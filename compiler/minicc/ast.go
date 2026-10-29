// Copied verbatim from ../lang/ast.go (minilang). Do not edit here; keep in sync with lang.

package minicc

// AST ノード。すべて位置 (Pos) を持つ。
type Node interface{ Pos() Pos }
type Expr = Node
type Stmt = Node

type base struct{ P Pos }

func (b base) Pos() Pos { return b.P }

// ---- 式 ----
type NumberLit struct {
	base
	V float64
}
type StringLit struct {
	base
	V string
}
type BoolLit struct {
	base
	V bool
}
type NilLit struct{ base }
type Ident struct {
	base
	Name string
}
type ArrayLit struct {
	base
	Elems []Expr
}
type MapLit struct {
	base
	Keys []string
	Vals []Expr
}
type FuncLit struct {
	base
	Name   string
	Params []string
	Body   []Stmt
}
type Unary struct {
	base
	Op string
	X  Expr
}
type Binary struct {
	base
	Op   string
	L, R Expr
}
type Assign struct {
	base
	Target Expr // *Ident か *Index
	Value  Expr
}
type Call struct {
	base
	Fn   Expr
	Args []Expr
}
type Index struct { // a[k] と a.k (k は StringLit) の両方
	base
	X, Key Expr
}

// ---- 文 ----
type LetStmt struct {
	base
	Name  string
	Const bool
	Value Expr
	IsFn  bool // fn name() {} 宣言 (再帰のため名前を先に束縛する)
}
type ExprStmt struct {
	base
	X Expr
}
type BlockStmt struct {
	base
	Body []Stmt
}
type IfStmt struct {
	base
	Cond Expr
	Then []Stmt
	Else Stmt // nil / *BlockStmt / *IfStmt
}
type WhileStmt struct {
	base
	Cond Expr
	Body []Stmt
}
type ForInStmt struct {
	base
	Var  string
	Iter Expr
	Body []Stmt
}
type ReturnStmt struct {
	base
	Value Expr // nil なら nil を返す
}
type BreakStmt struct{ base }
type ContinueStmt struct{ base }
