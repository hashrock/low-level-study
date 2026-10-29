package main

import "fmt"

// 型付けパス: パーサが作った木に型を付け、コード生成が「型を見て命令を選ぶだけ」で済むように書き換える。
//
//   - 各式に Ty を付ける (int + long → long、比較 → int、配列は要素へのポインタとして扱う…)
//   - ポインタ演算をバイト単位に直す: p + i → p + i*sizeof(*p)、p - q → (p - q) / sizeof(*p)
//   - 代入・引数・return の値を相手の型に変換する (NCast を挟む)
//   - sizeof expr を数値にする、メンバ名をオフセットに解決する
//   - 型エラーを報告する
//
// 書き換えは同じノードに 2 回適用しないよう、Ty が付いた式は飛ばす (パーサが定数式を先に型付けするため)。

type checker struct {
	fn  *Obj
	err error
}

func (c *checker) fail(n *Node, format string, args ...any) {
	if c.err == nil {
		c.err = errorAt(n.Tok.File, n.Tok.Line, format, args...)
	}
}

func (c *checker) function(fn *Obj) {
	c.fn = fn
	c.stmt(fn.Body)
}

func (c *checker) stmt(n *Node) {
	if n == nil {
		return
	}
	switch n.Kind {
	case NBlock:
		for _, s := range n.Body {
			c.stmt(s)
		}
	case NExprStmt:
		c.expr(n.L)
	case NIf, NWhile, NDoWhile, NFor, NSwitch:
		c.stmt(n.Init)
		if n.Cond != nil {
			c.expr(n.Cond)
			if !n.Cond.Ty.isScalar() && n.Cond.Ty.Kind != TyArray {
				c.fail(n.Cond, "condition must be a scalar, got %s", n.Cond.Ty)
			}
		}
		if n.Inc != nil {
			c.expr(n.Inc)
		}
		c.stmt(n.Then)
		c.stmt(n.Else)
	case NReturn:
		ret := c.fn.Ty.Ret
		if n.L == nil {
			if ret.Kind != TyVoid {
				c.fail(n, "missing return value")
			}
			return
		}
		c.expr(n.L)
		if ret.Kind == TyVoid {
			c.fail(n, "void function returns a value")
		}
		n.L = c.convert(n.L, ret)
	}
}

// convert は代入などで値を型 to に合わせる。整数どうしの縮小だけ NCast を挟めば十分
// (int → long はレジスタ上すでに符号拡張済みなので命令が要らない)。
func (c *checker) convert(n *Node, to *Type) *Node {
	from := n.Ty
	switch {
	case to.Kind == TyStruct || from.Kind == TyStruct:
		if to != from {
			c.fail(n, "incompatible struct types")
		}
		return n
	case !from.isScalar() && from.Kind != TyArray && from.Kind != TyFunc:
		c.fail(n, "cannot convert %s to %s", from, to)
	case to.isInteger() && from.isInteger() && to.Size < from.Size:
		return &Node{Kind: NCast, Ty: to, L: n, Tok: n.Tok}
	}
	return n
}

// arith は二項演算の結果の型 (通常の算術変換): どちらかが 8 バイトなら long、それ以外は int。
func arith(a, b *Type) *Type {
	if a.Size == 8 || b.Size == 8 {
		return tyLong
	}
	return tyInt
}

func num(v int64, tok *Token) *Node { return &Node{Kind: NNum, Val: v, Ty: tyLong, Tok: tok} }

func isLvalue(n *Node) bool {
	return n.Kind == NVar && n.Ty.Kind != TyFunc || n.Kind == NDeref || n.Kind == NMember
}

func (c *checker) expr(n *Node) {
	if n == nil || n.Ty != nil && n.Kind != NCast {
		return
	}
	for _, k := range []*Node{n.L, n.R, n.Cond, n.Then, n.Else} {
		c.expr(k)
	}
	for _, a := range n.Body {
		c.expr(a)
	}
	if c.err != nil {
		n.Ty = tyInt
		return
	}
	switch n.Kind {
	case NNum:
		n.Ty = tyInt
		if n.Val != int64(int32(n.Val)) {
			n.Ty = tyLong
		}
	case NVar:
		n.Ty = n.Var.Ty
	case NVaStart:
		n.Ty = pointerTo(tyLong)
	case NCast:
		if n.Ty.Kind != TyVoid && !n.Ty.isScalar() {
			c.fail(n, "cannot cast to %s", n.Ty)
		}
	case NComma:
		n.Ty = n.R.Ty
	case NSizeof:
		*n = *num(int64(n.L.Ty.Size), n.Tok)
	case NUnary:
		if !n.L.Ty.isScalar() && n.L.Ty.Kind != TyArray {
			c.fail(n, "invalid operand to unary %s", n.Op)
		}
		n.Ty = arith(n.L.Ty, tyInt)
		if n.Op == "!" {
			n.Ty = tyInt
		}
	case NAddr:
		if !isLvalue(n.L) && n.L.Ty.Kind != TyArray && n.L.Ty.Kind != TyFunc {
			c.fail(n, "cannot take the address of an rvalue")
		}
		n.Ty = pointerTo(n.L.Ty)
	case NDeref:
		if !n.L.Ty.isPtr() {
			if n.L.Ty.Kind == TyFunc { // (*f)(x) は f(x) と同じ
				*n = *n.L
				return
			}
			c.fail(n, "cannot dereference %s", n.L.Ty)
			n.Ty = tyInt
			return
		}
		n.Ty = n.L.Ty.Base
		if n.Ty.Kind == TyVoid {
			c.fail(n, "dereferencing void*")
		}
	case NMember:
		if n.L.Ty.Kind != TyStruct {
			c.fail(n, "member access on non-struct %s", n.L.Ty)
			n.Ty = tyInt
			return
		}
		for _, m := range n.L.Ty.Members {
			if m.Name == n.Name {
				n.Mem, n.Ty = m, m.Ty
				return
			}
		}
		c.fail(n, "no member named %s", n.Name)
		n.Ty = tyInt
	case NAssign:
		if !isLvalue(n.L) {
			c.fail(n, "cannot assign to this expression")
		}
		n.Ty = n.L.Ty
		if n.R == nil { // 0 埋め (ローカル配列・struct の初期化)
			return
		}
		if n.Op == "=" {
			n.R = c.convert(n.R, n.L.Ty)
			return
		}
		if n.L.Ty.Kind == TyPtr && (n.Op == "+" || n.Op == "-") { // p += n
			n.R = c.scale(n.R, n.L.Ty.Base.Size)
		} else if !n.L.Ty.isInteger() || !n.R.Ty.isInteger() {
			c.fail(n, "invalid operands to %s=", n.Op)
		}
	case NIncDec:
		if !isLvalue(n.L) || !n.L.Ty.isScalar() {
			c.fail(n, "invalid operand to %s%s", n.Op, n.Op)
		}
		n.Ty = n.L.Ty
		n.Val = 1
		if n.Ty.Kind == TyPtr {
			n.Val = int64(n.Ty.Base.Size)
		}
	case NCond:
		switch {
		case n.Then.Ty.Kind == TyVoid || n.Else.Ty.Kind == TyVoid:
			n.Ty = tyVoid
		case n.Then.Ty.isPtr():
			n.Ty = pointerTo(n.Then.Ty.Base)
		case n.Else.Ty.isPtr():
			n.Ty = pointerTo(n.Else.Ty.Base)
		case n.Then.Ty.Kind == TyStruct:
			n.Ty = n.Then.Ty
		default:
			n.Ty = arith(n.Then.Ty, n.Else.Ty)
		}
	case NCall:
		c.call(n)
	case NBinary:
		c.binary(n)
	}
}

// scale は整数 n を size 倍する (定数ならその場で掛ける)。
func (c *checker) scale(n *Node, size int) *Node {
	if !n.Ty.isInteger() {
		c.fail(n, "pointer arithmetic needs an integer")
		return n
	}
	if size == 1 {
		return n
	}
	if n.Kind == NNum {
		return num(n.Val*int64(size), n.Tok)
	}
	return &Node{Kind: NBinary, Op: "*", L: n, R: num(int64(size), n.Tok), Ty: tyLong, Tok: n.Tok}
}

func (c *checker) binary(n *Node) {
	l, r := n.L.Ty, n.R.Ty
	switch n.Op {
	case "+", "-":
		if r.isPtr() && n.Op == "+" && l.isInteger() { // 1 + p → p + 1
			n.L, n.R = n.R, n.L
			l, r = r, l
		}
		if l.isPtr() && r.isPtr() && n.Op == "-" { // p - q → (p - q) / sizeof(*p)
			sub := &Node{Kind: NBinary, Op: "-", L: n.L, R: n.R, Ty: tyLong, Tok: n.Tok}
			*n = Node{Kind: NBinary, Op: "/", L: sub, R: num(int64(l.Base.Size), n.Tok), Ty: tyLong, Tok: n.Tok}
			return
		}
		if l.isPtr() {
			n.R = c.scale(n.R, l.Base.Size)
			n.Ty = pointerTo(l.Base)
			return
		}
	case "==", "!=", "<", "<=", ">", ">=":
		if !(l.isScalar() || l.isPtr()) || !(r.isScalar() || r.isPtr()) {
			c.fail(n, "invalid operands to %s", n.Op)
		}
		n.Ty = tyInt
		return
	case "&&", "||":
		n.Ty = tyInt
		return
	case "<<", ">>":
		if !l.isInteger() || !r.isInteger() {
			c.fail(n, "invalid operands to %s", n.Op)
		}
		n.Ty = arith(l, tyInt) // シフトの型は左辺で決まる
		return
	}
	if !l.isInteger() || !r.isInteger() {
		c.fail(n, "invalid operands to %s (%s and %s)", n.Op, l, r)
	}
	n.Ty = arith(l, r)
}

func (c *checker) call(n *Node) {
	ft := n.L.Ty
	if ft.Kind == TyPtr && ft.Base.Kind == TyFunc { // 関数ポインタ経由
		ft = ft.Base
	}
	if ft.Kind != TyFunc {
		c.fail(n, "called object is not a function")
		n.Ty = tyInt
		return
	}
	if len(n.Body) < len(ft.Params) || len(n.Body) > len(ft.Params) && !ft.Variadic {
		c.fail(n, "wrong number of arguments: want %d, got %d", len(ft.Params), len(n.Body))
	}
	if len(n.Body) > 8 {
		c.fail(n, "more than 8 arguments are not supported")
	}
	for i, a := range n.Body {
		if a.Ty.Kind == TyStruct {
			c.fail(a, "passing structs by value is not supported (use a pointer)")
		}
		if i < len(ft.Params) {
			n.Body[i] = c.convert(a, ft.Params[i])
		}
	}
	n.Ty = ft.Ret
}

// evalConst はグローバル変数の初期値・配列の長さ・case ラベル用の定数式を計算する。
// 結果は「ラベル + 値」(ラベルが空なら単なる整数)。
func (c *checker) evalConst(n *Node) (int64, string, error) {
	c.expr(n)
	if c.err != nil {
		err := c.err
		c.err = nil
		return 0, "", err
	}
	return evalConst(n)
}

func evalConst(n *Node) (int64, string, error) {
	switch n.Kind {
	case NNum:
		return n.Val, "", nil
	case NVar:
		if n.Var.IsLocal || n.Ty.Kind != TyArray && n.Ty.Kind != TyFunc {
			break
		}
		return 0, n.Var.Name, nil // 配列名・関数名はアドレス
	case NAddr:
		return evalAddr(n.L)
	case NCast:
		v, label, err := evalConst(n.L)
		if label == "" && n.Ty.isInteger() {
			v = truncate(v, n.Ty)
		}
		return v, label, err
	case NCond:
		cond, _, err := evalConst(n.Cond)
		if err != nil {
			return 0, "", err
		}
		if cond != 0 {
			return evalConst(n.Then)
		}
		return evalConst(n.Else)
	case NUnary:
		v, label, err := evalConst(n.L)
		if err != nil || label != "" {
			break
		}
		switch n.Op {
		case "-":
			v = -v
		case "~":
			v = ^v
		case "!":
			v = b2i(v == 0)
		}
		return truncate(v, n.Ty), "", nil
	case NBinary:
		a, la, err1 := evalConst(n.L)
		b, lb, err2 := evalConst(n.R)
		if err1 != nil || err2 != nil || lb != "" || la != "" && n.Op != "+" && n.Op != "-" {
			break
		}
		if la != "" {
			if n.Op == "-" {
				b = -b
			}
			return a + b, la, nil
		}
		v, ok := foldBinary(n.Op, a, b)
		if !ok {
			break
		}
		return truncate(v, n.Ty), "", nil
	}
	return 0, "", fmt.Errorf("not a constant expression")
}

func evalAddr(n *Node) (int64, string, error) {
	switch n.Kind {
	case NVar:
		if !n.Var.IsLocal {
			return 0, n.Var.Name, nil
		}
	case NDeref:
		return evalConst(n.L)
	case NMember:
		v, label, err := evalAddr(n.L)
		return v + int64(n.Mem.Offset), label, err
	}
	return 0, "", fmt.Errorf("not a constant address")
}

func foldBinary(op string, a, b int64) (int64, bool) {
	switch op {
	case "+":
		return a + b, true
	case "-":
		return a - b, true
	case "*":
		return a * b, true
	case "/", "%":
		if b == 0 {
			return 0, false
		}
		if op == "/" {
			return a / b, true
		}
		return a % b, true
	case "<<":
		return a << uint(b&63), true
	case ">>":
		return a >> uint(b&63), true
	case "&":
		return a & b, true
	case "|":
		return a | b, true
	case "^":
		return a ^ b, true
	case "==":
		return b2i(a == b), true
	case "!=":
		return b2i(a != b), true
	case "<":
		return b2i(a < b), true
	case "<=":
		return b2i(a <= b), true
	case ">":
		return b2i(a > b), true
	case ">=":
		return b2i(a >= b), true
	case "&&":
		return b2i(a != 0 && b != 0), true
	case "||":
		return b2i(a != 0 || b != 0), true
	}
	return 0, false
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// truncate は値を型の幅に切り詰めて符号拡張する (レジスタ上の表現と同じにする)。
func truncate(v int64, t *Type) int64 {
	switch t.Size {
	case 1:
		return int64(int8(v))
	case 2:
		return int64(int16(v))
	case 4:
		return int64(int32(v))
	}
	return v
}
