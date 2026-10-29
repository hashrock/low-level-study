package minidb

import (
	"container/list"
	"fmt"
	"io"
	"os"
)

// ページャ: DB ファイルを固定長ページの配列として扱い、
// 最近使ったページをメモリにキャッシュする (LRU)。
// ページ ID は 0 から始まり、ファイル上のオフセットは ID * PageSize。

const (
	PageSize             = 4096
	DefaultCacheCapacity = 64 // キャッシュするページ数 (64 * 4KB = 256KB)
)

// File はページャが読み書きする「ファイル」。CLI では *os.File、
// ブラウザ (wasm) ではメモリ上のバイト列 (*MemFile) を使う。
type File interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
}

type Page struct {
	ID    uint32
	Data  [PageSize]byte
	Dirty bool // メモリ上で変更され、まだファイルに書いていない
}

type Pager struct {
	file     File
	numPages uint32
	capacity int
	cache    map[uint32]*list.Element // ページID -> LRU リストの要素
	lru      *list.List               // 先頭 = 最近使った, 末尾 = 追い出し候補

	// 観察用の累計カウンタ (動作には影響しない)
	counters PagerCounters
	trace    *pageTrace // nil でなければ 1 文の間のアクセスを記録する
}

// PagerCounters はページャ開始以来の累計。
type PagerCounters struct {
	Gets      uint64 `json:"gets"`      // Get の呼び出し回数
	Hits      uint64 `json:"hits"`      // そのうちキャッシュにあった回数
	Reads     uint64 `json:"reads"`     // ファイルからページを読んだ回数 (= キャッシュミス)
	Writes    uint64 `json:"writes"`    // ファイルへページを書いた回数
	Evictions uint64 `json:"evictions"` // キャッシュから追い出した回数
	Allocs    uint64 `json:"allocs"`    // 新しく確保したページ数
}

// pageTrace は 1 文の実行中に触ったページを記録する。
type pageTrace struct {
	start   PagerCounters
	touched []uint32 // 初めてアクセスした順
	seen    map[uint32]bool
	dirtied map[uint32]bool
	alloc   []uint32
	evicted []uint32
}

func OpenPager(path string) (*Pager, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	p, err := NewPager(f, st.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// NewPager は size バイトの既存ファイル f の上にページャを作る。
func NewPager(f File, size int64) (*Pager, error) {
	if size%PageSize != 0 {
		return nil, fmt.Errorf("file size %d is not a multiple of page size", size)
	}
	return &Pager{
		file:     f,
		numPages: uint32(size / PageSize),
		capacity: DefaultCacheCapacity,
		cache:    map[uint32]*list.Element{},
		lru:      list.New(),
	}, nil
}

// Get はページを返す。キャッシュに無ければディスクから読む。
// 返したポインタは次の Get/Allocate で追い出される可能性があるので、長く保持しないこと。
func (p *Pager) Get(id uint32) (*Page, error) {
	p.counters.Gets++
	p.touch(id)
	if e, ok := p.cache[id]; ok {
		p.counters.Hits++
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
	p.counters.Reads++
	return pg, p.add(pg)
}

// Allocate はファイル末尾に新しい (ゼロ埋めの) ページを確保する。
func (p *Pager) Allocate() (*Page, error) {
	pg := &Page{ID: p.numPages, Dirty: true}
	p.numPages++
	p.counters.Allocs++
	if p.trace != nil {
		p.trace.alloc = append(p.trace.alloc, pg.ID)
		p.trace.dirtied[pg.ID] = true
	}
	p.touch(pg.ID)
	return pg, p.add(pg)
}

func (p *Pager) MarkDirty(pg *Page) {
	pg.Dirty = true
	if p.trace != nil {
		p.trace.dirtied[pg.ID] = true
	}
}

func (p *Pager) add(pg *Page) error {
	p.cache[pg.ID] = p.lru.PushFront(pg)
	for p.lru.Len() > p.capacity {
		if err := p.evictOldest(); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pager) evictOldest() error {
	e := p.lru.Back()
	victim := e.Value.(*Page)
	if err := p.writePage(victim); err != nil {
		return err
	}
	p.lru.Remove(e)
	delete(p.cache, victim.ID)
	p.counters.Evictions++
	if p.trace != nil {
		p.trace.evicted = append(p.trace.evicted, victim.ID)
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
	p.counters.Writes++
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

// ---- 観察用 (内部状態を変えない) ----

func (p *Pager) touch(id uint32) {
	if t := p.trace; t != nil && !t.seen[id] {
		t.seen[id] = true
		t.touched = append(t.touched, id)
	}
}

func (p *Pager) startTrace() {
	p.trace = &pageTrace{start: p.counters, seen: map[uint32]bool{}, dirtied: map[uint32]bool{}}
}

func (p *Pager) stopTrace() *pageTrace {
	t := p.trace
	p.trace = nil
	return t
}

// peek はページの現在の内容 (キャッシュ上のものを優先) のコピーを返す。
// LRU の順序やカウンタは変えない。
func (p *Pager) peek(id uint32) ([]byte, error) {
	buf := make([]byte, PageSize)
	if e, ok := p.cache[id]; ok {
		copy(buf, e.Value.(*Page).Data[:])
		return buf, nil
	}
	if id >= p.numPages {
		return nil, fmt.Errorf("page %d out of range (%d pages)", id, p.numPages)
	}
	if _, err := p.file.ReadAt(buf, int64(id)*PageSize); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// SetCapacity はキャッシュできるページ数を変える (溢れた分はすぐ追い出す)。
func (p *Pager) SetCapacity(n int) error {
	if n < 1 {
		n = 1
	}
	p.capacity = n
	for p.lru.Len() > p.capacity {
		if err := p.evictOldest(); err != nil {
			return err
		}
	}
	return nil
}
