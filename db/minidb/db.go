package minidb

import (
	"encoding/binary"
	"fmt"
	"io"
)

// DB 本体とカタログ (テーブル定義の一覧)。カタログはページ 0 に保存する。
//
// ページ 0 のレイアウト:
//
//	[0:8]  マジック "MINIDB01"
//	[8:10] テーブル数 (uint16)
//	テーブルごとに:
//	  名前 (uint8 長さ + バイト列), B+tree のルートページ ID (uint32), カラム数 (uint8)
//	  カラムごとに: 名前 (uint8 長さ + バイト列), 型 (uint8), 主キーフラグ (uint8)

const catalogMagic = "MINIDB01"

type Column struct {
	Name       string
	Type       ColType
	PrimaryKey bool
}

type Table struct {
	Name    string
	Columns []Column
	PK      int    // 主キーカラムの位置
	Root    uint32 // B+tree のルートページ
	tree    *BTree
}

func (t *Table) colIndex(name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

type DB struct {
	pager  *Pager
	tables map[string]*Table
	order  []string // 作成順のテーブル名

	cur       *StmtStats   // 実行中の文の統計
	lastStats []*StmtStats // 直前の Exec で実行した各文の統計
}

// Open はパス path の DB ファイルを開く (無ければ作る)。
func Open(path string) (*DB, error) {
	p, err := OpenPager(path)
	if err != nil {
		return nil, err
	}
	db, err := openPager(p)
	if err != nil {
		p.file.Close()
	}
	return db, err
}

// OpenFile は任意の File (size バイト) の上に DB を開く。size が 0 なら新規作成。
func OpenFile(f File, size int64) (*DB, error) {
	p, err := NewPager(f, size)
	if err != nil {
		return nil, err
	}
	return openPager(p)
}

// OpenMem はメモリ上の DB を開く。data は既存の DB ファイルの中身 (nil なら新規)。
func OpenMem(data []byte) (*DB, error) {
	return OpenFile(NewMemFile(data), int64(len(data)))
}

func openPager(p *Pager) (*DB, error) {
	db := &DB{pager: p, tables: map[string]*Table{}}
	if p.numPages == 0 { // 新規ファイル: ページ 0 をカタログ用に確保
		if _, err := p.Allocate(); err != nil {
			return nil, err
		}
		if err := db.saveCatalog(); err != nil {
			return nil, err
		}
		return db, p.Flush()
	}
	return db, db.loadCatalog()
}

// Tables はテーブルを作成順に返す。
func (db *DB) Tables() []*Table {
	var ts []*Table
	for _, name := range db.order {
		ts = append(ts, db.tables[name])
	}
	return ts
}

// Snapshot は DB ファイル全体のバイト列を返す (dirty ページを書き出してから読む)。
func (db *DB) Snapshot() ([]byte, error) {
	if err := db.pager.Flush(); err != nil {
		return nil, err
	}
	buf := make([]byte, int(db.pager.numPages)*PageSize)
	if _, err := db.pager.file.ReadAt(buf, 0); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

func (db *DB) Close() error { return db.pager.Close() }

func (db *DB) table(name string) (*Table, error) {
	t, ok := db.tables[name]
	if !ok {
		return nil, fmt.Errorf("no such table: %s", name)
	}
	return t, nil
}

func (db *DB) addTable(t *Table) {
	t.tree = &BTree{pager: db.pager, root: t.Root}
	db.tables[t.Name] = t
	db.order = append(db.order, t.Name)
}

func appendStr(buf []byte, s string) []byte {
	return append(append(buf, byte(len(s))), s...)
}

func (db *DB) saveCatalog() error {
	le := binary.LittleEndian
	buf := []byte(catalogMagic)
	buf = le.AppendUint16(buf, uint16(len(db.order)))
	for _, name := range db.order {
		t := db.tables[name]
		buf = appendStr(buf, t.Name)
		buf = le.AppendUint32(buf, t.Root)
		buf = append(buf, byte(len(t.Columns)))
		for _, c := range t.Columns {
			buf = appendStr(buf, c.Name)
			pk := byte(0)
			if c.PrimaryKey {
				pk = 1
			}
			buf = append(buf, byte(c.Type), pk)
		}
	}
	if len(buf) > PageSize {
		return fmt.Errorf("catalog too large")
	}
	pg, err := db.pager.Get(0)
	if err != nil {
		return err
	}
	clear(pg.Data[:])
	copy(pg.Data[:], buf)
	db.pager.MarkDirty(pg)
	return nil
}

func (db *DB) loadCatalog() (err error) {
	defer func() { // 壊れたファイルで範囲外アクセスしたらエラーにする
		if r := recover(); r != nil {
			err = fmt.Errorf("corrupt catalog: %v", r)
		}
	}()
	pg, err := db.pager.Get(0)
	if err != nil {
		return err
	}
	buf := pg.Data[:]
	if string(buf[:8]) != catalogMagic {
		return fmt.Errorf("not a minidb file")
	}
	off := 8
	u8 := func() byte { off++; return buf[off-1] }
	str := func() string { n := int(u8()); off += n; return string(buf[off-n : off]) }

	ntables := int(binary.LittleEndian.Uint16(buf[off:]))
	off += 2
	for i := 0; i < ntables; i++ {
		t := &Table{Name: str()}
		t.Root = binary.LittleEndian.Uint32(buf[off:])
		off += 4
		ncols := int(u8())
		for j := 0; j < ncols; j++ {
			c := Column{Name: str(), Type: ColType(u8()), PrimaryKey: u8() == 1}
			if c.PrimaryKey {
				t.PK = j
			}
			t.Columns = append(t.Columns, c)
		}
		db.addTable(t)
	}
	return nil
}
