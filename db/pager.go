package main

import (
	"container/list"
	"fmt"
	"os"
)

// ページャ: DB ファイルを固定長ページの配列として扱い、
// 最近使ったページをメモリにキャッシュする (LRU)。
// ページ ID は 0 から始まり、ファイル上のオフセットは ID * PageSize。

const (
	PageSize      = 4096
	cacheCapacity = 64 // キャッシュするページ数 (64 * 4KB = 256KB)
)

type Page struct {
	ID    uint32
	Data  [PageSize]byte
	Dirty bool // メモリ上で変更され、まだファイルに書いていない
}

type Pager struct {
	file     *os.File
	numPages uint32
	cache    map[uint32]*list.Element // ページID -> LRU リストの要素
	lru      *list.List               // 先頭 = 最近使った, 末尾 = 追い出し候補
}

func OpenPager(path string) (*Pager, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size()%PageSize != 0 {
		return nil, fmt.Errorf("%s: file size is not a multiple of page size", path)
	}
	return &Pager{
		file:     f,
		numPages: uint32(st.Size() / PageSize),
		cache:    map[uint32]*list.Element{},
		lru:      list.New(),
	}, nil
}

// Get はページを返す。キャッシュに無ければディスクから読む。
// 返したポインタは次の Get/Allocate で追い出される可能性があるので、長く保持しないこと。
func (p *Pager) Get(id uint32) (*Page, error) {
	if e, ok := p.cache[id]; ok {
		p.lru.MoveToFront(e)
		return e.Value.(*Page), nil
	}
	if id >= p.numPages {
		return nil, fmt.Errorf("page %d out of range (%d pages)", id, p.numPages)
	}
	pg := &Page{ID: id}
	if _, err := p.file.ReadAt(pg.Data[:], int64(id)*PageSize); err != nil {
		return nil, err
	}
	return pg, p.add(pg)
}

// Allocate はファイル末尾に新しい (ゼロ埋めの) ページを確保する。
func (p *Pager) Allocate() (*Page, error) {
	pg := &Page{ID: p.numPages, Dirty: true}
	p.numPages++
	return pg, p.add(pg)
}

func (p *Pager) MarkDirty(pg *Page) { pg.Dirty = true }

func (p *Pager) add(pg *Page) error {
	p.cache[pg.ID] = p.lru.PushFront(pg)
	for p.lru.Len() > cacheCapacity {
		e := p.lru.Back()
		victim := e.Value.(*Page)
		if err := p.writePage(victim); err != nil {
			return err
		}
		p.lru.Remove(e)
		delete(p.cache, victim.ID)
	}
	return nil
}

func (p *Pager) writePage(pg *Page) error {
	if !pg.Dirty {
		return nil
	}
	if _, err := p.file.WriteAt(pg.Data[:], int64(pg.ID)*PageSize); err != nil {
		return err
	}
	pg.Dirty = false
	return nil
}

// Flush は全ての dirty ページを書き出し、fsync でディスクに永続化する。
func (p *Pager) Flush() error {
	for _, e := range p.cache {
		if err := p.writePage(e.Value.(*Page)); err != nil {
			return err
		}
	}
	return p.file.Sync()
}

func (p *Pager) Close() error {
	if err := p.Flush(); err != nil {
		return err
	}
	return p.file.Close()
}
