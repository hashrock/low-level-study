package main

import (
	"strings"
	"testing"
)

func TestLexer(t *testing.T) {
	toks, err := Lex("SELECT a FROM T WHERE s = 'it''s' AND x <> -3;")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tk := range toks {
		got = append(got, tk.Text)
	}
	want := "SELECT a FROM t WHERE s = it's AND x != - 3 ; "
	if strings.Join(got, " ") != want {
		t.Errorf("got %q, want %q", strings.Join(got, " "), want)
	}
}

func TestParseSelect(t *testing.T) {
	stmts, err := Parse(`SELECT u.name, p.title FROM users JOIN posts ON users.id = posts.user_id
		WHERE age >= 20 AND (name = 'bob' OR id < 3) ORDER BY id DESC LIMIT 5;`)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if s.From != "users" || s.Join == nil || s.Join.Table != "posts" || s.Limit != 5 ||
		!s.OrderBy.Desc || s.OrderBy.Column.Name != "id" || len(s.Columns) != 2 {
		t.Errorf("unexpected AST: %+v", s)
	}
	if got := ExprString(s.Where); got != "((age >= 20) AND ((name = 'bob') OR (id < 3)))" {
		t.Errorf("where = %s", got)
	}
	if got := ExprString(s.Join.On); got != "(users.id = posts.user_id)" {
		t.Errorf("on = %s", got)
	}
}

func TestParseOthers(t *testing.T) {
	stmts, err := Parse(`
		CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO t VALUES (1, 'a'), (2, NULL);
		INSERT INTO t (name) VALUES ('c');
		UPDATE t SET name = 'z', id = 9 WHERE id = 1;
		DELETE FROM t WHERE id > 1 OR name = 'x';
		DELETE FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 6 {
		t.Fatalf("got %d statements", len(stmts))
	}
	c := stmts[0].(*CreateTableStmt)
	if len(c.Columns) != 2 || !c.Columns[0].PrimaryKey || c.Columns[1].Type != TypeText {
		t.Errorf("create: %+v", c)
	}
	if ins := stmts[1].(*InsertStmt); len(ins.Rows) != 2 || ins.Columns != nil {
		t.Errorf("insert: %+v", ins)
	}
	if u := stmts[3].(*UpdateStmt); len(u.Sets) != 2 || ExprString(u.Where) != "(id = 1)" {
		t.Errorf("update: %+v", u)
	}
	if d := stmts[5].(*DeleteStmt); d.Where != nil {
		t.Errorf("delete: %+v", d)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{
		"SELECT FROM t",
		"SELECT * FROM",
		"CREATE TABLE t (id FLOAT)",
		"INSERT INTO t VALUES (1",
		"SELECT * FROM t WHERE a = 'unterminated",
		"SELECT * FROM t LIMIT x",
		"DROP TABLE t",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
}
