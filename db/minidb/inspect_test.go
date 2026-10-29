package minidb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fillUsers(t *testing.T, db *DB, n int) {
	t.Helper()
	query(t, db, "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)")
	var vals []string
	for i := 1; i <= n; i++ {
		vals = append(vals, fmt.Sprintf("(%d, '%s', %d)", (i*37)%n+1, strings.Repeat("n", 20+i%50), i%90))
	}
	query(t, db, "INSERT INTO users VALUES "+strings.Join(vals, ","))
}

func TestMemDBAndSnapshot(t *testing.T) {
	db, err := OpenMem(nil)
	if err != nil {
		t.Fatal(err)
	}
	fillUsers(t, db, 500)
	data, err := db.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%PageSize != 0 || len(data) < 3*PageSize {
		t.Fatalf("snapshot size %d", len(data))
	}
	// スナップショットはそのまま普通のファイルとして開ける
	path := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if got := query(t, db2, "SELECT id FROM users WHERE id = 250"); got != "250" {
		t.Errorf("reopen: %q", got)
	}
	// メモリからも開き直せる
	db3, err := OpenMem(append([]byte{}, data...))
	if err != nil {
		t.Fatal(err)
	}
	if got := query(t, db3, "SELECT id FROM users WHERE id = 7"); got != "7" {
		t.Errorf("reopen mem: %q", got)
	}
	if _, err := OpenMem(make([]byte, 100)); err == nil {
		t.Error("expected error for bad size")
	}
	if _, err := OpenMem(make([]byte, PageSize)); err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestTreeDumpAndStats(t *testing.T) {
	db, _ := OpenMem(nil)
	fillUsers(t, db, 500)
	tree, err := db.TreeDump("users")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Type != "internal" || len(tree.Children) < 2 {
		t.Fatalf("root should be internal with children: %+v", tree.Type)
	}
	// 葉の next を辿るとキーが昇順に全部出てくる
	leaves := map[uint32]*TreeNode{}
	var walk func(n *TreeNode)
	walk = func(n *TreeNode) {
		if n.Type == "leaf" {
			leaves[n.Page] = n
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(tree)
	first := tree
	for first.Type != "leaf" {
		first = first.Children[0]
	}
	count := 0
	prev := int64(0)
	for n := first; ; n = leaves[n.Next] {
		if n.Used+n.Free != PageSize || n.Used > PageSize {
			t.Errorf("page %d used=%d free=%d", n.Page, n.Used, n.Free)
		}
		for _, k := range n.Keys {
			if k <= prev {
				t.Fatalf("keys out of order")
			}
			prev = k
			count++
		}
		if n.Next == 0 {
			break
		}
	}
	if count != 500 {
		t.Errorf("leaf chain has %d keys", count)
	}
	if _, err := json.Marshal(tree); err != nil {
		t.Fatal(err)
	}

	// 主キー検索 vs 全件スキャン
	query(t, db, "SELECT * FROM users WHERE id = 5")
	pk := db.LastStats()[0]
	query(t, db, "SELECT * FROM users WHERE name = 'x'")
	scan := db.LastStats()[0]
	if pk.Access[0].Method != AccessPKLookup || pk.RowsExamined != 1 || pk.PageGets != 2 {
		t.Errorf("pk stats: %+v %+v", pk, pk.Access[0])
	}
	if scan.Access[0].Method != AccessFullScan || scan.RowsExamined != 500 || len(scan.Touched) != len(leaves)+1 {
		t.Errorf("scan stats: %+v", scan)
	}
	if !strings.Contains(scan.SQL, "name = 'x'") {
		t.Errorf("sql text: %q", scan.SQL)
	}

	// ルートの分割で木が高くなるところを観察
	db2, _ := OpenMem(nil)
	query(t, db2, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)")
	split := false
	for i := 1; i <= 40 && !split; i++ {
		query(t, db2, fmt.Sprintf("INSERT INTO t VALUES (%d, '%s')", i, strings.Repeat("v", 300)))
		st := db2.LastStats()[0]
		if st.RootSplits > 0 {
			split = true
			if st.Splits != 1 || len(st.Allocated) != 2 || st.Access[0].Method != AccessPKInsert {
				t.Errorf("root split stats: %+v", st)
			}
		}
	}
	if !split {
		t.Error("root never split")
	}
}

func TestPageInfoAndCache(t *testing.T) {
	db, _ := OpenMem(nil)
	fillUsers(t, db, 300)
	cat, err := db.PageInfo(0)
	if err != nil || cat.Kind != "catalog" || len(cat.Cells) != 1 || !strings.Contains(cat.Cells[0].Text, "id INTEGER PK") {
		t.Fatalf("catalog: %v %+v", err, cat)
	}
	root, _ := db.PageInfo(1)
	if root.Kind != "internal" || root.Table != "users" || *root.Cells[0].Child == 0 {
		t.Fatalf("root: %+v", root)
	}
	leaf, _ := db.PageInfo(*root.Cells[0].Child)
	if leaf.Kind != "leaf" || len(leaf.Cells) == 0 || len(leaf.Cells[0].Values) != 3 {
		t.Fatalf("leaf: %+v", leaf)
	}
	if leaf.Cells[0].Values[0] != fmt.Sprint(*leaf.Cells[0].Key) {
		t.Errorf("first value should be the key: %v", leaf.Cells[0].Values)
	}
	// バイト範囲は重ならず昇順
	last := 0
	for _, r := range leaf.Ranges {
		if r.Start < last || r.End < r.Start {
			t.Fatalf("bad range %+v", r)
		}
		last = r.End
	}
	if leaf.Used != last {
		t.Errorf("used %d, ranges end %d", leaf.Used, last)
	}
	if _, err := db.PageInfo(9999); err == nil {
		t.Error("expected out of range")
	}

	// キャッシュを小さくすると追い出しとディスク読み込みが起きる
	cs := db.CacheState()
	before := cs.Counters.Gets
	if cs.Capacity != DefaultCacheCapacity || len(cs.LRU) == 0 {
		t.Fatalf("cache: %+v", cs)
	}
	if err := db.SetCacheCapacity(2); err != nil {
		t.Fatal(err)
	}
	query(t, db, "SELECT * FROM users WHERE age = 1")
	st := db.LastStats()[0]
	if st.DiskReads == 0 || st.Evictions == 0 {
		t.Errorf("expected disk reads with tiny cache: %+v", st)
	}
	cs = db.CacheState()
	if len(cs.LRU) != 2 || cs.Counters.Gets <= before {
		t.Errorf("cache after: %+v", cs)
	}
	// 覗き見はカウンタを変えない
	g := db.CacheState().Counters.Gets
	db.TreeDump("users")
	db.PageInfo(1)
	if db.CacheState().Counters.Gets != g {
		t.Error("introspection changed counters")
	}
}
