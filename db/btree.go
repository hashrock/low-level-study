package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// B+tree: キー (int64 の主キー) → 値 (エンコード済みの行)。
//
//   - 1 ノード = 1 ページ。
//   - 葉ノード (leaf) が全てのキーと値を持ち、next ポインタで右隣の葉に連結される。
//     → 全件スキャンは「一番左の葉まで降りて、next を辿る」だけ。
//   - 内部ノード (internal) は keys[i] と children[i+1] を持つ。
//     children[i] 以下のキーは keys[i] 未満、children[i+1] 以下は keys[i] 以上。
//   - ノードは読むたびに Go の構造体にデコードし、変更したらページにエンコードし直す。
//     (本物の DB はページ上で直接操作するが、学習用に分かりやすさを優先)
//
// ページ上のレイアウト:
//
//	[0]    ノード種別 (1=leaf, 2=internal)
//	[1:3]  キー数 n (uint16)
//	[3:7]  next: 右隣の葉のページ ID (leaf のみ, 0=なし)
//	[7:]   leaf    : n 個の (key int64, vlen uint16, value [vlen]byte)
//	       internal: child0 uint32, 続いて n 個の (key int64, child uint32)

const (
	nodeLeaf       = 1
	nodeInternal   = 2
	nodeHeaderSize = 7
	// 1 行の最大サイズ。分割後の各半分が必ず 1 ページに収まるよう小さめに制限する。
	MaxValueSize = 1000
)

var ErrDuplicateKey = errors.New("duplicate primary key")

type node struct {
	leaf     bool
	keys     []int64
	vals     [][]byte // leaf のみ
	children []uint32 // internal のみ (len = len(keys)+1)
	next     uint32   // leaf のみ
}

func (n *node) size() int {
	s := nodeHeaderSize
	if n.leaf {
		for _, v := range n.vals {
			s += 8 + 2 + len(v)
		}
		return s
	}
	return s + 4 + 12*len(n.keys)
}

func (n *node) encode(buf []byte) {
	clear(buf)
	le := binary.LittleEndian
	buf[0] = nodeInternal
	if n.leaf {
		buf[0] = nodeLeaf
	}
	le.PutUint16(buf[1:], uint16(len(n.keys)))
	le.PutUint32(buf[3:], n.next)
	off := nodeHeaderSize
	if n.leaf {
		for i, k := range n.keys {
			le.PutUint64(buf[off:], uint64(k))
			le.PutUint16(buf[off+8:], uint16(len(n.vals[i])))
			off += 10 + copy(buf[off+10:], n.vals[i])
		}
		return
	}
	le.PutUint32(buf[off:], n.children[0])
	off += 4
	for i, k := range n.keys {
		le.PutUint64(buf[off:], uint64(k))
		le.PutUint32(buf[off+8:], n.children[i+1])
		off += 12
	}
}

func decodeNode(buf []byte) (*node, error) {
	le := binary.LittleEndian
	count := int(le.Uint16(buf[1:]))
	n := &node{next: le.Uint32(buf[3:])}
	off := nodeHeaderSize
	switch buf[0] {
	case nodeLeaf:
		n.leaf = true
		for i := 0; i < count; i++ {
			k := int64(le.Uint64(buf[off:]))
			l := int(le.Uint16(buf[off+8:]))
			off += 10
			v := make([]byte, l)
			copy(v, buf[off:off+l])
			off += l
			n.keys = append(n.keys, k)
			n.vals = append(n.vals, v)
		}
	case nodeInternal:
		n.children = append(n.children, le.Uint32(buf[off:]))
		off += 4
		for i := 0; i < count; i++ {
			n.keys = append(n.keys, int64(le.Uint64(buf[off:])))
			n.children = append(n.children, le.Uint32(buf[off+8:]))
			off += 12
		}
	default:
		return nil, fmt.Errorf("invalid node type %d", buf[0])
	}
	return n, nil
}

// search: 葉の中で key 以上の最初の位置と、key が存在するかを返す (二分探索)。
func (n *node) search(key int64) (int, bool) {
	i := sort.Search(len(n.keys), func(j int) bool { return n.keys[j] >= key })
	return i, i < len(n.keys) && n.keys[i] == key
}

// childIndex: 内部ノードで key を含む子の位置。
func (n *node) childIndex(key int64) int {
	return sort.Search(len(n.keys), func(j int) bool { return n.keys[j] > key })
}

func insertAt[T any](s []T, i int, v T) []T {
	s = append(s, v)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// BTree はルートページ ID だけを持つ。ルートのページ ID は分割しても変わらない。
type BTree struct {
	pager *Pager
	root  uint32
}

// CreateBTree は空の葉 1 枚からなる木を作り、ルートのページ ID を返す。
func CreateBTree(p *Pager) (uint32, error) {
	pg, err := p.Allocate()
	if err != nil {
		return 0, err
	}
	t := &BTree{pager: p, root: pg.ID}
	return pg.ID, t.write(pg.ID, &node{leaf: true})
}

func (t *BTree) read(id uint32) (*node, error) {
	pg, err := t.pager.Get(id)
	if err != nil {
		return nil, err
	}
	return decodeNode(pg.Data[:])
}

func (t *BTree) write(id uint32, n *node) error {
	pg, err := t.pager.Get(id)
	if err != nil {
		return err
	}
	n.encode(pg.Data[:])
	t.pager.MarkDirty(pg)
	return nil
}

// findLeaf はルートから key が入るべき葉まで降りる。
func (t *BTree) findLeaf(key int64) (uint32, *node, error) {
	id := t.root
	for {
		n, err := t.read(id)
		if err != nil || n.leaf {
			return id, n, err
		}
		id = n.children[n.childIndex(key)]
	}
}

func (t *BTree) Get(key int64) ([]byte, bool, error) {
	_, n, err := t.findLeaf(key)
	if err != nil {
		return nil, false, err
	}
	if i, found := n.search(key); found {
		return n.vals[i], true, nil
	}
	return nil, false, nil
}

func (t *BTree) Insert(key int64, val []byte) error {
	if len(val) > MaxValueSize {
		return fmt.Errorf("row too large (%d bytes, max %d)", len(val), MaxValueSize)
	}
	sep, right, split, err := t.insert(t.root, key, val)
	if err != nil || !split {
		return err
	}
	// ルートが分割された: ルートの中身 (左半分) を新しいページへ移し、
	// ルートページを「左・右の 2 つの子を持つ内部ノード」に作り替える。
	// こうするとルートのページ ID が変わらず、カタログを書き換えずに済む。
	left, err := t.read(t.root)
	if err != nil {
		return err
	}
	pg, err := t.pager.Allocate()
	if err != nil {
		return err
	}
	if err := t.write(pg.ID, left); err != nil {
		return err
	}
	return t.write(t.root, &node{keys: []int64{sep}, children: []uint32{pg.ID, right}})
}

// insert は再帰的に挿入する。ノードが溢れたら分割し、
// (親に挿入すべき区切りキー, 新しい右ノードのID, true) を返す。
func (t *BTree) insert(id uint32, key int64, val []byte) (int64, uint32, bool, error) {
	n, err := t.read(id)
	if err != nil {
		return 0, 0, false, err
	}
	if n.leaf {
		i, found := n.search(key)
		if found {
			return 0, 0, false, ErrDuplicateKey
		}
		n.keys = insertAt(n.keys, i, key)
		n.vals = insertAt(n.vals, i, val)
	} else {
		ci := n.childIndex(key)
		sep, right, split, err := t.insert(n.children[ci], key, val)
		if err != nil || !split {
			return 0, 0, false, err
		}
		n.keys = insertAt(n.keys, ci, sep)
		n.children = insertAt(n.children, ci+1, right)
	}
	if n.size() <= PageSize {
		return 0, 0, false, t.write(id, n)
	}
	return t.split(id, n)
}

func (t *BTree) split(id uint32, n *node) (int64, uint32, bool, error) {
	pg, err := t.pager.Allocate()
	if err != nil {
		return 0, 0, false, err
	}
	right := &node{leaf: n.leaf}
	var sep int64
	if n.leaf {
		// バイト数がおよそ半分になる位置で分ける (値の長さが可変なので)
		mid := len(n.keys) / 2
		acc, total := nodeHeaderSize, n.size()
		for i, v := range n.vals {
			acc += 10 + len(v)
			if acc >= total/2 {
				mid = min(i+1, len(n.keys)-1)
				break
			}
		}
		right.keys = append(right.keys, n.keys[mid:]...)
		right.vals = append(right.vals, n.vals[mid:]...)
		right.next, n.next = n.next, pg.ID // 葉の連結リストに右ノードを差し込む
		n.keys, n.vals = n.keys[:mid], n.vals[:mid]
		sep = right.keys[0] // 葉の分割: 区切りキーは「コピー」して親へ
	} else {
		mid := len(n.keys) / 2
		sep = n.keys[mid] // 内部ノードの分割: 区切りキーは親へ「移動」
		right.keys = append(right.keys, n.keys[mid+1:]...)
		right.children = append(right.children, n.children[mid+1:]...)
		n.keys, n.children = n.keys[:mid], n.children[:mid+1]
	}
	if err := t.write(id, n); err != nil {
		return 0, 0, false, err
	}
	return sep, pg.ID, true, t.write(pg.ID, right)
}

// Delete は葉からキーを取り除くだけ (ノードの併合・再配分はしない)。
func (t *BTree) Delete(key int64) (bool, error) {
	id, n, err := t.findLeaf(key)
	if err != nil {
		return false, err
	}
	i, found := n.search(key)
	if !found {
		return false, nil
	}
	n.keys = append(n.keys[:i], n.keys[i+1:]...)
	n.vals = append(n.vals[:i], n.vals[i+1:]...)
	return true, t.write(id, n)
}

// Scan は全キーを昇順に訪問する。fn が false を返すと中断。
func (t *BTree) Scan(fn func(key int64, val []byte) (bool, error)) error {
	id := t.root
	for { // 一番左の葉まで降りる
		n, err := t.read(id)
		if err != nil {
			return err
		}
		if n.leaf {
			break
		}
		id = n.children[0]
	}
	for id != 0 { // 葉の連結リストを辿る (ページ 0 はカタログなので葉にはならない)
		n, err := t.read(id)
		if err != nil {
			return err
		}
		for i, k := range n.keys {
			if cont, err := fn(k, n.vals[i]); err != nil || !cont {
				return err
			}
		}
		id = n.next
	}
	return nil
}

// MaxKey は最大のキーを返す (自動採番用)。単純化のため全件スキャンする。
func (t *BTree) MaxKey() (int64, bool, error) {
	var max int64
	found := false
	err := t.Scan(func(k int64, _ []byte) (bool, error) {
		max, found = k, true
		return true, nil
	})
	return max, found, err
}
