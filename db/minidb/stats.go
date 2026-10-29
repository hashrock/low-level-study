package minidb

import (
	"fmt"
	"sort"
	"strings"
)

// 文ごとの統計: 何ページ触ったか、主キー検索か全件スキャンか、何行見たか。
// 実行の結果には影響しない (観察のためだけ)。

const (
	AccessPKLookup = "pk-lookup" // 主キーの等値条件で B+tree を 1 回だけ引いた
	AccessFullScan = "full-scan" // 葉を next で辿って全行を見た
	AccessPKInsert = "pk-insert" // 主キーで挿入位置を探して葉に入れた
)

// Access は 1 つのテーブルへのアクセス方法。
type Access struct {
	Table  string `json:"table"`
	Method string `json:"method"`
	Key    *int64 `json:"key,omitempty"` // pk-lookup / 1 回だけの pk-insert のときのキー
	Count  int    `json:"count"`         // 同じ方法で何回アクセスしたか
	Rows   int    `json:"rows"`          // 取り出した (デコードした) 行数
	Note   string `json:"note,omitempty"`
}

type StmtStats struct {
	SQL     string    `json:"sql"`
	Kind    string    `json:"kind"` // CREATE / INSERT / SELECT / UPDATE / DELETE
	Error   string    `json:"error,omitempty"`
	Message string    `json:"message,omitempty"`
	Access  []*Access `json:"access"`

	RowsExamined int `json:"rowsExamined"` // テーブルから読み出した行数の合計
	RowsResult   int `json:"rowsResult"`   // SELECT が返した行数

	PageGets   uint64   `json:"pageGets"`   // ページャへの Get 回数
	CacheHits  uint64   `json:"cacheHits"`  // そのうちキャッシュにあった回数
	DiskReads  uint64   `json:"diskReads"`  // ファイルから読んだページ数
	DiskWrites uint64   `json:"diskWrites"` // ファイルへ書いたページ数 (文末の flush を含む)
	Evictions  uint64   `json:"evictions"`
	Touched    []uint32 `json:"touched"`    // 触ったページ (初回アクセス順)
	Dirtied    []uint32 `json:"dirtied"`    // 変更したページ
	Allocated  []uint32 `json:"allocated"`  // 新しく確保したページ
	Splits     int      `json:"splits"`     // ノード分割の回数
	RootSplits int      `json:"rootSplits"` // ルート分割 (木が高くなった) 回数
}

func stmtKind(s Statement) string {
	name := fmt.Sprintf("%T", s)
	name = strings.TrimPrefix(name, "*minidb.")
	name = strings.TrimSuffix(name, "Stmt")
	switch name {
	case "CreateTable":
		return "CREATE"
	}
	return strings.ToUpper(name)
}

func (db *DB) beginStats(s Statement, sql string) {
	db.cur = &StmtStats{SQL: sql, Kind: stmtKind(s)}
	for _, t := range db.tables {
		db.cur.Splits -= t.tree.splits
		db.cur.RootSplits -= t.tree.rootSplits
	}
	db.pager.startTrace()
}

func (db *DB) endStats(r *Result, err error) {
	st := db.cur
	db.cur = nil
	tr := db.pager.stopTrace()
	c := db.pager.counters
	st.PageGets = c.Gets - tr.start.Gets
	st.CacheHits = c.Hits - tr.start.Hits
	st.DiskReads = c.Reads - tr.start.Reads
	st.DiskWrites = c.Writes - tr.start.Writes
	st.Evictions = c.Evictions - tr.start.Evictions
	st.Touched = append([]uint32{}, tr.touched...)
	st.Allocated = append([]uint32{}, tr.alloc...)
	st.Dirtied = []uint32{}
	for id := range tr.dirtied {
		st.Dirtied = append(st.Dirtied, id)
	}
	sort.Slice(st.Dirtied, func(i, j int) bool { return st.Dirtied[i] < st.Dirtied[j] })
	for _, t := range db.tables { // CREATE で増えたテーブルは beginStats 時に無いが、splits は 0 なので問題ない
		st.Splits += t.tree.splits
		st.RootSplits += t.tree.rootSplits
	}
	if st.Access == nil {
		st.Access = []*Access{}
	}
	for _, a := range st.Access {
		st.RowsExamined += a.Rows
	}
	if r != nil {
		st.Message = r.Message
		st.RowsResult = len(r.Rows)
	}
	if err != nil {
		st.Error = err.Error()
	}
	db.lastStats = append(db.lastStats, st)
}

// noteAccess はテーブルへのアクセスを記録し、行数を数えるための *Access を返す。
// 同じテーブル・同じ方法のアクセスは 1 つにまとめる。
func (db *DB) noteAccess(t *Table, method string, key *int64) *Access {
	if db.cur == nil {
		return &Access{}
	}
	for _, a := range db.cur.Access {
		if a.Table == t.Name && a.Method == method {
			a.Count++
			a.Key = nil
			return a
		}
	}
	a := &Access{Table: t.Name, Method: method, Count: 1}
	if key != nil {
		k := *key
		a.Key = &k
	}
	db.cur.Access = append(db.cur.Access, a)
	return a
}

// LastStats は直前の Exec で実行した各文の統計を返す (エラーになった文も含む)。
func (db *DB) LastStats() []*StmtStats { return db.lastStats }
