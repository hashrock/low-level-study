package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// query は SQL を実行し、最後の結果を "1,alice;2,bob" のような文字列にする。
func query(t *testing.T, db *DB, sql string) string {
	t.Helper()
	res, err := db.Exec(sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	r := res[len(res)-1]
	if r.Columns == nil {
		return r.Message
	}
	var rows []string
	for _, row := range r.Rows {
		var cells []string
		for _, v := range row {
			cells = append(cells, v.String())
		}
		rows = append(rows, strings.Join(cells, ","))
	}
	return strings.Join(rows, ";")
}

func TestEndToEnd(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query(t, db, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER);
		INSERT INTO users VALUES (3, 'carol', 41), (1, 'alice', 30), (2, 'bob', 25);
		INSERT INTO users (name, age) VALUES ('dave', NULL);`)

	cases := []struct{ sql, want string }{
		{"SELECT * FROM users", "1,alice,30;2,bob,25;3,carol,41;4,dave,NULL"},
		{"SELECT name FROM users WHERE id = 2", "bob"},
		{"SELECT name FROM users WHERE age > 26 AND age < 40 OR name = 'dave'", "alice;dave"},
		{"SELECT name, age FROM users ORDER BY age DESC LIMIT 2", "carol,41;alice,30"},
		{"SELECT name FROM users WHERE age != 30 ORDER BY name", "bob;carol"},
		{"UPDATE users SET age = 26 WHERE name = 'bob'", "UPDATE 1"},
		{"UPDATE users SET id = 10 WHERE id = 4", "UPDATE 1"},
		{"DELETE FROM users WHERE age <= 30", "DELETE 2"},
		{"SELECT id, name, age FROM users", "3,carol,41;10,dave,NULL"},
	}
	for _, c := range cases {
		if got := query(t, db, c.sql); got != c.want {
			t.Errorf("%s\n got: %s\nwant: %s", c.sql, got, c.want)
		}
	}

	for _, bad := range []string{
		"INSERT INTO users VALUES (3, 'dup', 1)",
		"INSERT INTO users VALUES ('x', 'y', 1)",
		"SELECT nope FROM users",
		"SELECT * FROM missing",
		"UPDATE users SET id = 3 WHERE id = 10",
	} {
		if _, err := db.Exec(bad); err == nil {
			t.Errorf("expected error: %s", bad)
		}
	}
}

func TestJoin(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "join.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query(t, db, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title TEXT);
		INSERT INTO users VALUES (1, 'alice'), (2, 'bob');
		INSERT INTO posts VALUES (10, 1, 'hello'), (11, 2, 'hi'), (12, 1, 'again');`)
	got := query(t, db, `SELECT users.name, posts.title FROM posts JOIN users ON posts.user_id = users.id
		WHERE users.name = 'alice' ORDER BY posts.id DESC`)
	if got != "alice,again;alice,hello" {
		t.Errorf("join: %s", got)
	}
}

// 多数の行を入れて B+tree を何段にも分割させ、閉じて開き直しても読めることを確認する。
func TestPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	query(t, db, "CREATE TABLE kv (k INTEGER PRIMARY KEY, v TEXT)")
	const n = 5000
	// 逆順・飛び飛びで挿入して、分割のいろいろなパターンを踏ませる
	// 500 行ずつ 1 つの INSERT 文にまとめる (文ごとに fsync するため)
	var vals []string
	for i := n - 1; i >= 0; i-- {
		k := (i * 7919) % n
		vals = append(vals, fmt.Sprintf("(%d, '%s')", k, strings.Repeat("x", k%200)))
		if len(vals) == 500 {
			query(t, db, "INSERT INTO kv VALUES "+strings.Join(vals, ","))
			vals = nil
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec("SELECT k, v FROM kv")
	if err != nil {
		t.Fatal(err)
	}
	rows := res[0].Rows
	if len(rows) != n {
		t.Fatalf("got %d rows, want %d", len(rows), n)
	}
	for i, r := range rows {
		if r[0].Int != int64(i) || len(r[1].Str) != i%200 {
			t.Fatalf("row %d = %v", i, r)
		}
	}
	if got := query(t, db, "SELECT v FROM kv WHERE k = 4321"); len(got) != 4321%200 {
		t.Errorf("point lookup: len %d", len(got))
	}
	if db.pager.numPages < 100 {
		t.Errorf("expected many pages, got %d", db.pager.numPages)
	}
	// 削除・更新後も開き直して確認
	query(t, db, "DELETE FROM kv WHERE k >= 100")
	query(t, db, "UPDATE kv SET v = 'changed' WHERE k = 5")
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := query(t, db, "SELECT k FROM kv WHERE k > 97"); got != "98;99" {
		t.Errorf("after delete: %s", got)
	}
	if got := query(t, db, "SELECT v FROM kv WHERE k = 5"); got != "changed" {
		t.Errorf("after update: %s", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// 内部ノードの分割まで起こす量のキーを B+tree に直接入れて、順序と点検索を確認する。
func TestBTreeDeep(t *testing.T) {
	p, err := OpenPager(filepath.Join(t.TempDir(), "bt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Allocate() // ページ 0 (カタログ用) を空けておく
	root, err := CreateBTree(p)
	if err != nil {
		t.Fatal(err)
	}
	tree := &BTree{pager: p, root: root}
	const n = 60000
	val := []byte(strings.Repeat("v", 100))
	for i := 0; i < n; i++ {
		if err := tree.Insert(int64((i*7919)%n), val); err != nil {
			t.Fatal(err)
		}
	}
	depth := 1
	for id := root; ; depth++ {
		nd, _ := tree.read(id)
		if nd.leaf {
			break
		}
		id = nd.children[0]
	}
	if depth < 3 {
		t.Errorf("depth = %d, want >= 3", depth)
	}
	next := int64(0)
	tree.Scan(func(k int64, _ []byte) (bool, error) {
		if k != next {
			t.Fatalf("scan: got %d want %d", k, next)
		}
		next++
		return true, nil
	})
	if next != n {
		t.Errorf("scanned %d keys", next)
	}
	if _, found, _ := tree.Get(31337); !found {
		t.Error("Get(31337) not found")
	}
	if err := tree.Insert(5, val); err != ErrDuplicateKey {
		t.Errorf("want duplicate error, got %v", err)
	}
}
