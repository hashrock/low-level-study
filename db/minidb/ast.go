package minidb

import "strings"

// AST (抽象構文木)。パーサが作り、エグゼキュータが解釈する。

type Statement interface{ stmt() }

type CreateTableStmt struct {
	Name    string
	Columns []Column
}

type InsertStmt struct {
	Table   string
	Columns []string // 省略時は nil (= 全カラムを定義順に)
	Rows    [][]Expr
}

type SelectStmt struct {
	Star    bool
	Columns []ColumnRef
	From    string
	Join    *JoinClause
	Where   Expr
	OrderBy *OrderBy
	Limit   int // -1 = 制限なし
}

type JoinClause struct {
	Table string
	On    Expr
}

type OrderBy struct {
	Column ColumnRef
	Desc   bool
}

type UpdateStmt struct {
	Table string
	Sets  []Assignment
	Where Expr
}

type Assignment struct {
	Column string
	Value  Expr
}

type DeleteStmt struct {
	Table string
	Where Expr
}

func (*CreateTableStmt) stmt() {}
func (*InsertStmt) stmt()      {}
func (*SelectStmt) stmt()      {}
func (*UpdateStmt) stmt()      {}
func (*DeleteStmt) stmt()      {}

// 式
type Expr interface{ expr() }

type Literal struct{ Value Value }

type ColumnRef struct{ Table, Name string } // Table は省略可

type BinaryExpr struct {
	Op          string // = != < > <= >= AND OR
	Left, Right Expr
}

func (*Literal) expr()    {}
func (*ColumnRef) expr()  {}
func (*BinaryExpr) expr() {}

func (c ColumnRef) String() string {
	if c.Table == "" {
		return c.Name
	}
	return c.Table + "." + c.Name
}

// デバッグ・テスト用に式を文字列化する
func ExprString(e Expr) string {
	switch e := e.(type) {
	case *Literal:
		if e.Value.Kind == KindText {
			return "'" + strings.ReplaceAll(e.Value.Str, "'", "''") + "'"
		}
		return e.Value.String()
	case *ColumnRef:
		return e.String()
	case *BinaryExpr:
		return "(" + ExprString(e.Left) + " " + e.Op + " " + ExprString(e.Right) + ")"
	}
	return "?"
}
