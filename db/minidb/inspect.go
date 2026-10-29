package minidb

import (
	"encoding/binary"
	"fmt"
)

// 内部状態を覗くための API (可視化用)。どれもページャの LRU 順やカウンタを変えない。

type ColumnInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	PrimaryKey bool   `json:"pk"`
}

type TableInfo struct {
	Name    string       `json:"name"`
	Root    uint32       `json:"root"`
	Columns []ColumnInfo `json:"columns"`
}

// TableInfos はテーブル一覧 (作成順) とそれぞれのルートページ。
func (db *DB) TableInfos() []TableInfo {
	out := []TableInfo{}
	for _, t := range db.Tables() {
		ti := TableInfo{Name: t.Name, Root: t.Root}
		for _, c := range t.Columns {
			ti.Columns = append(ti.Columns, ColumnInfo{c.Name, c.Type.String(), c.PrimaryKey})
		}
		out = append(out, ti)
	}
	return out
}

// TreeNode は B+tree の 1 ノード (= 1 ページ)。
type TreeNode struct {
	Page     uint32      `json:"page"`
	Type     string      `json:"type"` // "leaf" / "internal"
	Keys     []int64     `json:"keys"`
	ChildIDs []uint32    `json:"childIds,omitempty"` // internal のみ
	Children []*TreeNode `json:"children,omitempty"`
	Next     uint32      `json:"next"` // leaf のみ (0 = なし)
	Used     int         `json:"used"` // 使用バイト数 (ヘッダ込み)
	Free     int         `json:"free"`
}

// TreeDump はテーブルの B+tree 全体を返す。
func (db *DB) TreeDump(table string) (*TreeNode, error) {
	t, err := db.table(table)
	if err != nil {
		return nil, err
	}
	return db.dumpNode(t.Root, 0)
}

func (db *DB) readNodeQuiet(id uint32) (*node, error) {
	buf, err := db.pager.peek(id)
	if err != nil {
		return nil, err
	}
	return decodeNode(buf)
}

func (db *DB) dumpNode(id uint32, depth int) (*TreeNode, error) {
	if depth > 32 {
		return nil, fmt.Errorf("tree too deep (corrupt?)")
	}
	n, err := db.readNodeQuiet(id)
	if err != nil {
		return nil, fmt.Errorf("page %d: %w", id, err)
	}
	tn := &TreeNode{Page: id, Type: "internal", Keys: n.keys, Next: n.next, Used: n.size()}
	if tn.Keys == nil {
		tn.Keys = []int64{}
	}
	tn.Free = PageSize - tn.Used
	if n.leaf {
		tn.Type = "leaf"
		return tn, nil
	}
	tn.ChildIDs = n.children
	for _, c := range n.children {
		cn, err := db.dumpNode(c, depth+1)
		if err != nil {
			return nil, err
		}
		tn.Children = append(tn.Children, cn)
	}
	return tn, nil
}

// PageBytes はページの現在の内容 (キャッシュ上の未書き出しの変更も含む)。
func (db *DB) PageBytes(id uint32) ([]byte, error) { return db.pager.peek(id) }

// ---- ページの注釈付きダンプ ----

// ByteRange はページ上のバイト範囲 [Start, End) の意味。
type ByteRange struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Kind  string `json:"kind"` // type, count, next, key, vlen, value, child, magic, name, root, ...
	Label string `json:"label"`
	Cell  int    `json:"cell"` // 属するセル番号 (-1 = ヘッダ)
}

// Cell はページ上の 1 エントリをデコードしたもの。
type Cell struct {
	Index  int      `json:"index"`
	Start  int      `json:"start"`
	End    int      `json:"end"`
	Key    *int64   `json:"key,omitempty"`
	Child  *uint32  `json:"child,omitempty"`
	Values []string `json:"values,omitempty"` // 葉: 行のカラム値
	Text   string   `json:"text,omitempty"`   // カタログ: テーブル定義
}

type PageInfo struct {
	ID      uint32      `json:"id"`
	Kind    string      `json:"kind"` // catalog / leaf / internal / empty / unknown
	Table   string      `json:"table,omitempty"`
	Columns []string    `json:"columns,omitempty"`
	Used    int         `json:"used"`
	Free    int         `json:"free"`
	Cached  bool        `json:"cached"`
	Dirty   bool        `json:"dirty"`
	Ranges  []ByteRange `json:"ranges"`
	Cells   []Cell      `json:"cells"`
	Data    []byte      `json:"data"` // JSON では base64
}

// pageOwners は各ページがどのテーブルの B+tree に属するかを返す。
func (db *DB) pageOwners() map[uint32]*Table {
	m := map[uint32]*Table{}
	var walk func(t *Table, id uint32, depth int)
	walk = func(t *Table, id uint32, depth int) {
		if depth > 32 || m[id] != nil {
			return
		}
		m[id] = t
		n, err := db.readNodeQuiet(id)
		if err != nil || n.leaf {
			return
		}
		for _, c := range n.children {
			walk(t, c, depth+1)
		}
	}
	for _, t := range db.Tables() {
		walk(t, t.Root, 0)
	}
	return m
}

// PageInfo はページを注釈付きでデコードする (README のレイアウト通り)。
func (db *DB) PageInfo(id uint32) (info *PageInfo, err error) {
	buf, err := db.pager.peek(id)
	if err != nil {
		return nil, err
	}
	info = &PageInfo{ID: id, Data: buf, Ranges: []ByteRange{}, Cells: []Cell{}}
	if e, ok := db.pager.cache[id]; ok {
		info.Cached = true
		info.Dirty = e.Value.(*Page).Dirty
	}
	defer func() { // 壊れたページでも落ちないように
		if r := recover(); r != nil {
			info.Kind = "unknown"
			err = nil
		}
		info.Free = PageSize - info.Used
	}()
	if id == 0 {
		db.annotateCatalog(info, buf)
		return info, nil
	}
	if t := db.pageOwners()[id]; t != nil {
		info.Table = t.Name
		for _, c := range t.Columns {
			info.Columns = append(info.Columns, c.Name)
		}
	}
	add := func(s, e int, kind, label string, cell int) {
		info.Ranges = append(info.Ranges, ByteRange{s, e, kind, label, cell})
	}
	le := binary.LittleEndian
	switch buf[0] {
	case nodeLeaf, nodeInternal:
	default:
		info.Kind = "empty"
		for _, b := range buf {
			if b != 0 {
				info.Kind = "unknown"
				break
			}
		}
		return info, nil
	}
	n := int(le.Uint16(buf[1:]))
	add(0, 1, "type", "種別", -1)
	add(1, 3, "count", fmt.Sprintf("キー数 n=%d", n), -1)
	off := nodeHeaderSize
	if buf[0] == nodeLeaf {
		info.Kind = "leaf"
		add(3, 7, "next", fmt.Sprintf("next=%d", le.Uint32(buf[3:])), -1)
		for i := 0; i < n; i++ {
			k := int64(le.Uint64(buf[off:]))
			l := int(le.Uint16(buf[off+8:]))
			c := Cell{Index: i, Start: off, End: off + 10 + l, Key: &k}
			add(off, off+8, "key", fmt.Sprintf("key=%d", k), i)
			add(off+8, off+10, "vlen", fmt.Sprintf("vlen=%d", l), i)
			add(off+10, off+10+l, "value", "行データ", i)
			ncols := len(info.Columns)
			if ncols == 0 {
				ncols = countValues(buf[off+10 : off+10+l])
			}
			if row, err := decodeRow(buf[off+10:off+10+l], ncols); err == nil {
				for _, v := range row {
					s := v.String()
					if v.Kind == KindText {
						s = "'" + s + "'"
					}
					c.Values = append(c.Values, s)
				}
			}
			info.Cells = append(info.Cells, c)
			off += 10 + l
		}
	} else {
		info.Kind = "internal"
		add(3, 7, "next", "next (内部ノードでは未使用)", -1)
		c0 := le.Uint32(buf[off:])
		add(off, off+4, "child", fmt.Sprintf("child0=%d", c0), 0)
		info.Cells = append(info.Cells, Cell{Index: 0, Start: off, End: off + 4, Child: &c0})
		off += 4
		for i := 0; i < n; i++ {
			k := int64(le.Uint64(buf[off:]))
			ch := le.Uint32(buf[off+8:])
			add(off, off+8, "key", fmt.Sprintf("key=%d", k), i+1)
			add(off+8, off+12, "child", fmt.Sprintf("child=%d", ch), i+1)
			info.Cells = append(info.Cells, Cell{Index: i + 1, Start: off, End: off + 12, Key: &k, Child: &ch})
			off += 12
		}
	}
	info.Used = off
	return info, nil
}

// countValues はカラム数が分からないときに、行のバイト列にいくつ値があるか数える。
func countValues(b []byte) int {
	n, off := 0, 0
	for off < len(b) {
		switch Kind(b[off]) {
		case KindNull:
			off++
		case KindInt:
			off += 9
		case KindText:
			if off+3 > len(b) {
				return n
			}
			off += 3 + int(binary.LittleEndian.Uint16(b[off+1:]))
		default:
			return n
		}
		n++
	}
	return n
}

func (db *DB) annotateCatalog(info *PageInfo, buf []byte) {
	info.Kind = "catalog"
	add := func(s, e int, kind, label string, cell int) {
		info.Ranges = append(info.Ranges, ByteRange{s, e, kind, label, cell})
	}
	le := binary.LittleEndian
	add(0, 8, "magic", "マジック "+string(buf[:8]), -1)
	nt := int(le.Uint16(buf[8:]))
	add(8, 10, "count", fmt.Sprintf("テーブル数=%d", nt), -1)
	off := 10
	for i := 0; i < nt; i++ {
		start := off
		l := int(buf[off])
		name := string(buf[off+1 : off+1+l])
		add(off, off+1+l, "name", "テーブル名 "+name, i)
		off += 1 + l
		root := le.Uint32(buf[off:])
		add(off, off+4, "root", fmt.Sprintf("ルートページ=%d", root), i)
		off += 4
		nc := int(buf[off])
		add(off, off+1, "ncols", fmt.Sprintf("カラム数=%d", nc), i)
		off++
		text := fmt.Sprintf("%s (root %d):", name, root)
		for j := 0; j < nc; j++ {
			l := int(buf[off])
			cname := string(buf[off+1 : off+1+l])
			add(off, off+1+l, "colname", "カラム名 "+cname, i)
			off += 1 + l
			typ, pk := ColType(buf[off]), buf[off+1] == 1
			label := typ.String()
			if pk {
				label += " PK"
			}
			add(off, off+2, "coltype", "型/主キー "+label, i)
			off += 2
			text += " " + cname + " " + label
			if j < nc-1 {
				text += ","
			}
		}
		info.Cells = append(info.Cells, Cell{Index: i, Start: start, End: off, Text: text})
	}
	info.Used = off
}

// ---- キャッシュの状態 ----

type CachedPage struct {
	ID    uint32 `json:"id"`
	Dirty bool   `json:"dirty"`
}

type CacheState struct {
	Capacity int           `json:"capacity"`
	NumPages uint32        `json:"numPages"` // ファイル上のページ数 (確保済み)
	LRU      []CachedPage  `json:"lru"`      // 先頭 = 最近使った、末尾 = 次に追い出される
	Counters PagerCounters `json:"counters"`
}

func (db *DB) CacheState() CacheState {
	p := db.pager
	cs := CacheState{Capacity: p.capacity, NumPages: p.numPages, LRU: []CachedPage{}, Counters: p.counters}
	for e := p.lru.Front(); e != nil; e = e.Next() {
		pg := e.Value.(*Page)
		cs.LRU = append(cs.LRU, CachedPage{pg.ID, pg.Dirty})
	}
	return cs
}

// SetCacheCapacity はページキャッシュの容量 (ページ数) を変える。
func (db *DB) SetCacheCapacity(n int) error { return db.pager.SetCapacity(n) }
