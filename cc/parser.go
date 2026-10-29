package main

import (
	"fmt"
	"strings"
)

// 再帰下降パーサ。名前 (変数・typedef・enum 定数・struct タグ) の解決もここで行う。
// C は「使う前に宣言する」言語なので、読みながらスコープを積めば一度で解決できる。
// 式の型付けは check.go が別のパスで行う。

type scopeEntry struct {
	v       *Obj
	typedef *Type
	enum    *int64
}

type scope struct {
	names map[string]*scopeEntry
	tags  map[string]*Type
}

type Program struct {
	Globals []*Obj // グローバル変数と関数 (出現順)
}

type parser struct {
	toks    []Token
	pos     int
	scopes  []*scope
	prog    *Program
	fn      *Obj  // 解析中の関数
	sw      *Node // 解析中の switch (case を集める)
	nstr    int
	checker *checker
	loops   int // 入れ子の while/for/do の数 (continue の検査)
	breaks  int // loops + switch の数 (break の検査)
}

type declAttr struct{ isTypedef, isStatic, isExtern bool }

type parseError struct{ err error }

// Parse は複数のトークン列 (先頭は libc.h の前置宣言) を 1 つの翻訳単位として解析する。
func Parse(toks []Token) (prog *Program, err error) {
	p := &parser{toks: toks, prog: &Program{}}
	p.checker = &checker{}
	defer func() {
		if r := recover(); r != nil {
			pe, ok := r.(parseError)
			if !ok {
				panic(r)
			}
			err = pe.err
		}
	}()
	p.enterScope()
	for p.peek().Kind != TEOF {
		p.topLevel()
	}
	return p.prog, nil
}

// ---- トークン操作 ----

func (p *parser) peek() *Token { return &p.toks[p.pos] }
func (p *parser) next() *Token { t := &p.toks[p.pos]; p.pos++; return t }
func (p *parser) is(s string) bool {
	t := p.peek()
	return (t.Kind == TPunct || t.Kind == TKeyword) && t.Text == s
}
func (p *parser) consume(s string) bool {
	if p.is(s) {
		p.pos++
		return true
	}
	return false
}
func (p *parser) expect(s string) {
	if !p.consume(s) {
		p.fail(p.peek(), "expected %q", s)
	}
}
func (p *parser) ident() *Token {
	t := p.next()
	if t.Kind != TIdent {
		p.fail(t, "expected identifier")
	}
	return t
}
func (p *parser) fail(t *Token, format string, args ...any) {
	near := t.Text
	if t.Kind == TEOF {
		near = "end of file"
	}
	panic(parseError{errorAt(t.File, t.Line, "%s (near %q)", fmt.Sprintf(format, args...), near)})
}

// ---- スコープ ----

func (p *parser) enterScope() {
	p.scopes = append(p.scopes, &scope{map[string]*scopeEntry{}, map[string]*Type{}})
}
func (p *parser) leaveScope() { p.scopes = p.scopes[:len(p.scopes)-1] }
func (p *parser) top() *scope { return p.scopes[len(p.scopes)-1] }

func (p *parser) lookup(name string) *scopeEntry {
	for i := len(p.scopes) - 1; i >= 0; i-- {
		if e := p.scopes[i].names[name]; e != nil {
			return e
		}
	}
	return nil
}

func (p *parser) lookupTag(name string) *Type {
	for i := len(p.scopes) - 1; i >= 0; i-- {
		if t := p.scopes[i].tags[name]; t != nil {
			return t
		}
	}
	return nil
}

func (p *parser) newLocal(name string, ty *Type) *Obj {
	v := &Obj{Name: name, Ty: ty, IsLocal: true}
	p.fn.Locals = append(p.fn.Locals, v)
	p.top().names[name] = &scopeEntry{v: v}
	return v
}

func (p *parser) newGlobal(name string, ty *Type) *Obj {
	if e := p.scopes[0].names[name]; e != nil && e.v != nil { // 再宣言 (プロトタイプ → 定義など)
		e.v.Ty = ty
		return e.v
	}
	v := &Obj{Name: name, Ty: ty}
	p.prog.Globals = append(p.prog.Globals, v)
	p.scopes[0].names[name] = &scopeEntry{v: v}
	return v
}

// ---- 宣言 ----

func (p *parser) isTypeName() bool {
	t := p.peek()
	if t.Kind == TKeyword {
		switch t.Text {
		case "void", "char", "short", "int", "long", "struct", "enum", "typedef", "static", "extern", "const", "signed", "unsigned":
			return true
		}
	}
	if t.Kind == TIdent {
		e := p.lookup(t.Text)
		return e != nil && e.typedef != nil
	}
	return false
}

// declspec は "static const unsigned long" のような型指定子の並びを読む。
func (p *parser) declspec(attr *declAttr) *Type {
	var ty *Type
	nlong, nshort, nint := 0, 0, 0
	for p.isTypeName() {
		t := p.next()
		switch t.Text {
		case "typedef", "static", "extern":
			if attr == nil {
				p.fail(t, "storage class not allowed here")
			}
			attr.isTypedef = attr.isTypedef || t.Text == "typedef"
			attr.isStatic = attr.isStatic || t.Text == "static"
			attr.isExtern = attr.isExtern || t.Text == "extern"
			continue
		case "const", "signed":
			continue
		case "unsigned":
			p.fail(t, "unsigned types are not supported")
		case "long":
			nlong++
			continue
		case "short":
			nshort++
			continue
		case "int":
			nint++
			continue
		}
		if ty != nil {
			p.fail(t, "duplicate type")
		}
		switch t.Text {
		case "void":
			ty = tyVoid
		case "char":
			ty = tyChar
		case "struct":
			ty = p.structDecl()
		case "enum":
			ty = p.enumDecl()
		default:
			ty = p.lookup(t.Text).typedef
		}
	}
	switch {
	case nlong > 0:
		ty = tyLong
	case nshort > 0:
		ty = tyShort
	case nint > 0 || ty == nil:
		if ty == nil && nint == 0 {
			p.fail(p.peek(), "expected a type")
		}
		ty = tyInt
	}
	return ty
}

func (p *parser) structDecl() *Type {
	var tag string
	if p.peek().Kind == TIdent {
		tag = p.next().Text
	}
	if tag != "" && !p.is("{") {
		if t := p.lookupTag(tag); t != nil {
			return t
		}
		t := &Type{Kind: TyStruct, Size: 0, Align: 1} // 前方宣言 (ポインタ経由でだけ使える)
		p.top().tags[tag] = t
		return t
	}
	p.expect("{")
	t := p.top().tags[tag]
	if t == nil || t.Complete {
		t = &Type{Kind: TyStruct, Align: 1}
	}
	if tag != "" {
		p.top().tags[tag] = t // 先に登録して自己参照 (struct node *next) を許す
	}
	for !p.consume("}") {
		base := p.declspec(nil)
		for first := true; !p.consume(";"); first = false {
			if !first {
				p.expect(",")
			}
			ty, name := p.declarator(base)
			if ty.Kind == TyStruct && !ty.Complete || ty.Kind == TyVoid {
				p.fail(name, "incomplete member type")
			}
			t.Members = append(t.Members, &Member{Name: name.Text, Ty: ty})
		}
	}
	layoutStruct(t)
	return t
}

func (p *parser) enumDecl() *Type {
	if p.peek().Kind == TIdent {
		p.next()
		if !p.is("{") {
			return tyInt
		}
	}
	p.expect("{")
	var val int64
	for !p.consume("}") {
		name := p.ident()
		if p.consume("=") {
			val = p.constExpr()
		}
		v := val
		p.top().names[name.Text] = &scopeEntry{enum: &v}
		val++
		if !p.consume(",") {
			p.expect("}")
			break
		}
	}
	return tyInt
}

// declarator は "*" の並び、名前 (省略可: 型名だけのとき)、後置の [] () を読む。
// "int (*fp)(int)" のような括弧付きは、括弧の中を飛ばして外側の後置部分を先に読み、
// その型を「中身の型」として括弧の中をもう一度読む (chibicc と同じ方法)。
func (p *parser) declarator(ty *Type) (*Type, *Token) {
	for p.consume("*") {
		ty = pointerTo(ty)
		for p.consume("const") {
		}
	}
	if p.is("(") && !p.isTypeNameAt(p.pos+1) && !p.isAt(p.pos+1, ")") {
		start := p.pos + 1
		depth := 0
		for {
			if p.is("(") {
				depth++
			} else if p.is(")") {
				depth--
			}
			p.next()
			if depth == 0 {
				break
			}
		}
		ty = p.typeSuffix(ty)
		end := p.pos
		p.pos = start
		ty, name := p.declarator(ty)
		p.expect(")")
		p.pos = end
		return ty, name
	}
	var name *Token
	if p.peek().Kind == TIdent {
		name = p.next()
	} else {
		name = &Token{Kind: TIdent, File: p.peek().File, Line: p.peek().Line}
	}
	return p.typeSuffix(ty), name
}

func (p *parser) isAt(i int, s string) bool { return p.toks[i].Text == s && p.toks[i].Kind == TPunct }
func (p *parser) isTypeNameAt(i int) bool {
	save := p.pos
	p.pos = i
	defer func() { p.pos = save }()
	return p.isTypeName()
}

func (p *parser) typeSuffix(ty *Type) *Type {
	if p.consume("(") {
		var params []*Type
		var names []*Token
		variadic := false
		if p.is("void") && p.isAt(p.pos+1, ")") {
			p.pos++
		}
		for !p.consume(")") {
			if len(params) > 0 {
				p.expect(",")
			}
			if p.consume("...") {
				variadic = true
				p.expect(")")
				break
			}
			pt, name := p.declarator(p.declspec(nil))
			if pt.Kind == TyArray { // 引数の配列はポインタ
				pt = pointerTo(pt.Base)
			}
			params = append(params, pt)
			names = append(names, name)
		}
		ft := funcType(ty, params, variadic)
		ft.ParamNames = names
		return ft
	}
	if p.consume("[") {
		n := -1
		if !p.is("]") {
			n = int(p.constExpr())
		}
		p.expect("]")
		elem := p.typeSuffix(ty)
		return arrayOf(elem, n)
	}
	return ty
}

func (p *parser) typeName() *Type {
	ty, _ := p.declarator(p.declspec(nil))
	return ty
}

func (p *parser) topLevel() {
	var attr declAttr
	base := p.declspec(&attr)
	if p.consume(";") {
		return
	}
	for first := true; ; first = false {
		if !first {
			p.expect(",")
		}
		ty, name := p.declarator(base)
		if name.Text == "" {
			p.fail(name, "missing name")
		}
		if attr.isTypedef {
			p.top().names[name.Text] = &scopeEntry{typedef: ty}
		} else if ty.Kind == TyFunc {
			if ty.Ret.Kind == TyStruct || ty.Ret.Kind == TyArray {
				p.fail(name, "returning a struct or array is not supported (use a pointer)")
			}
			fn := p.newGlobal(name.Text, ty)
			fn.IsStatic = attr.isStatic
			if p.is("{") {
				p.funcDef(fn, ty.ParamNames)
				return
			}
		} else {
			v := p.newGlobal(name.Text, ty)
			v.IsStatic = attr.isStatic
			v.IsDef = !attr.isExtern
			if p.consume("=") {
				p.globalInit(v)
			}
		}
		if p.consume(";") {
			return
		}
	}
}

func (p *parser) funcDef(fn *Obj, names []*Token) {
	if fn.IsDef {
		p.fail(p.peek(), "redefinition of %s", fn.Name)
	}
	fn.IsDef = true
	p.fn = fn
	p.enterScope()
	for i, pt := range fn.Ty.Params {
		if names[i].Text == "" {
			p.fail(names[i], "parameter name omitted")
		}
		fn.Params = append(fn.Params, p.newLocal(names[i].Text, pt))
	}
	if len(fn.Params) > 8 {
		p.fail(names[8], "more than 8 parameters are not supported")
	}
	if fn.Ty.Variadic {
		fn.VaArea = p.newLocal("__va_area", arrayOf(tyLong, 8))
	}
	fn.Body = p.compound()
	p.leaveScope()
	p.checker.function(fn)
	if p.checker.err != nil {
		panic(parseError{p.checker.err})
	}
	p.fn = nil
}

// ---- 初期化子 ----

// initItem は初期化子を平らにしたもの: 変数の先頭から off バイトの位置に、型 ty の値 expr を置く。
type initItem struct {
	off  int
	ty   *Type
	expr *Node
}

func (p *parser) initializer(ty *Type, off int, items *[]initItem) {
	t := p.peek()
	switch {
	case ty.Kind == TyArray && ty.Base.Kind == TyChar && t.Kind == TStr:
		p.next()
		s := t.Text + "\x00"
		if ty.Len < 0 {
			*ty = *arrayOf(ty.Base, len(s))
		}
		for i := 0; i < ty.Len && i < len(s); i++ {
			*items = append(*items, initItem{off + i, tyChar, &Node{Kind: NNum, Val: int64(s[i]), Tok: t}})
		}
	case ty.Kind == TyArray:
		p.expect("{")
		i := 0
		for ; !p.consume("}"); i++ {
			if ty.Len >= 0 && i >= ty.Len {
				p.fail(p.peek(), "too many initializers")
			}
			p.initializer(ty.Base, off+i*ty.Base.Size, items)
			if !p.consume(",") {
				p.expect("}")
				i++
				break
			}
		}
		if ty.Len < 0 {
			*ty = *arrayOf(ty.Base, i)
		}
	case ty.Kind == TyStruct && p.is("{"):
		p.next()
		for i := 0; !p.consume("}"); i++ {
			if i >= len(ty.Members) {
				p.fail(p.peek(), "too many initializers")
			}
			m := ty.Members[i]
			p.initializer(m.Ty, off+m.Offset, items)
			if !p.consume(",") {
				p.expect("}")
				break
			}
		}
	case p.consume("{"): // int x = {1};
		p.initializer(ty, off, items)
		p.consume(",")
		p.expect("}")
	default:
		*items = append(*items, initItem{off, ty, p.assign()})
	}
}

func (p *parser) globalInit(v *Obj) {
	var items []initItem
	p.initializer(v.Ty, 0, &items)
	v.InitBytes = make([]byte, v.Ty.Size)
	for _, it := range items {
		val, label, err := p.checker.evalConst(it.expr)
		if err != nil {
			p.fail(it.expr.Tok, "%v", err)
		}
		if label != "" {
			if it.ty.Size != 8 {
				p.fail(it.expr.Tok, "address in a non-pointer initializer")
			}
			v.InitRels = append(v.InitRels, Reloc{it.off, label, val})
			continue
		}
		for i := 0; i < it.ty.Size; i++ { // リトルエンディアンで書く
			v.InitBytes[it.off+i] = byte(val >> (8 * i))
		}
	}
}

// localInit はローカル変数の初期化子を代入文の並びにする。
// 配列・struct は最初に全体を 0 にしてから、書かれた要素だけ代入する。
func (p *parser) localInit(v *Obj, tok *Token) *Node {
	var items []initItem
	p.initializer(v.Ty, 0, &items)
	blk := &Node{Kind: NBlock, Tok: tok}
	if v.Ty.Kind == TyArray || v.Ty.Kind == TyStruct {
		if len(items) == 1 && items[0].ty.Kind == TyStruct { // struct s = other;
			return exprStmt(&Node{Kind: NAssign, Op: "=", L: varNode(v, tok), R: items[0].expr, Tok: tok})
		}
		blk.Body = append(blk.Body, exprStmt(&Node{Kind: NAssign, Op: "=", L: varNode(v, tok), Tok: tok})) // R=nil: 0 埋め
	}
	for _, it := range items {
		var lhs *Node
		if v.Ty.Kind == TyArray || v.Ty.Kind == TyStruct {
			// *(T*)((char*)&v + off)
			addr := &Node{Kind: NCast, Ty: pointerTo(tyChar), L: &Node{Kind: NAddr, L: varNode(v, tok), Tok: tok}, Tok: tok}
			sum := &Node{Kind: NBinary, Op: "+", L: addr, R: &Node{Kind: NNum, Val: int64(it.off), Tok: tok}, Tok: tok}
			lhs = &Node{Kind: NDeref, L: &Node{Kind: NCast, Ty: pointerTo(it.ty), L: sum, Tok: tok}, Tok: tok}
		} else {
			lhs = varNode(v, tok)
		}
		blk.Body = append(blk.Body, exprStmt(&Node{Kind: NAssign, Op: "=", L: lhs, R: it.expr, Tok: tok}))
	}
	return blk
}

func varNode(v *Obj, tok *Token) *Node { return &Node{Kind: NVar, Var: v, Tok: tok} }
func exprStmt(e *Node) *Node           { return &Node{Kind: NExprStmt, L: e, Tok: e.Tok} }

// ---- 文 ----

func (p *parser) compound() *Node {
	blk := &Node{Kind: NBlock, Tok: p.peek()}
	p.expect("{")
	p.enterScope()
	for !p.consume("}") {
		if p.isTypeName() {
			blk.Body = append(blk.Body, p.localDecl())
		} else {
			blk.Body = append(blk.Body, p.stmt())
		}
	}
	p.leaveScope()
	return blk
}

func (p *parser) localDecl() *Node {
	var attr declAttr
	base := p.declspec(&attr)
	blk := &Node{Kind: NBlock, Tok: p.peek()}
	for first := true; !p.consume(";"); first = false {
		if !first {
			p.expect(",")
		}
		ty, name := p.declarator(base)
		if attr.isTypedef {
			p.top().names[name.Text] = &scopeEntry{typedef: ty}
			continue
		}
		if ty.Kind == TyVoid || name.Text == "" {
			p.fail(name, "bad declaration")
		}
		if attr.isStatic || attr.isExtern || ty.Kind == TyFunc {
			p.fail(name, "static/extern/function declarations inside functions are not supported")
		}
		v := p.newLocal(name.Text, ty)
		if p.consume("=") {
			blk.Body = append(blk.Body, p.localInit(v, name))
		}
		if ty.Kind == TyArray && ty.Len < 0 {
			p.fail(name, "array size missing")
		}
	}
	return blk
}

func (p *parser) stmt() *Node {
	t := p.peek()
	n := &Node{Tok: t}
	switch {
	case p.is("{"):
		return p.compound()
	case p.consume(";"):
		n.Kind = NBlock
	case p.consume("if"):
		n.Kind = NIf
		p.expect("(")
		n.Cond = p.expr()
		p.expect(")")
		n.Then = p.stmt()
		if p.consume("else") {
			n.Else = p.stmt()
		}
	case p.consume("while"):
		n.Kind = NWhile
		p.expect("(")
		n.Cond = p.expr()
		p.expect(")")
		n.Then = p.loopBody()
	case p.consume("do"):
		n.Kind = NDoWhile
		n.Then = p.loopBody()
		p.expect("while")
		p.expect("(")
		n.Cond = p.expr()
		p.expect(")")
		p.expect(";")
	case p.consume("for"):
		n.Kind = NFor
		p.expect("(")
		p.enterScope()
		if p.isTypeName() {
			n.Init = p.localDecl()
		} else if !p.consume(";") {
			n.Init = exprStmt(p.expr())
			p.expect(";")
		}
		if !p.is(";") {
			n.Cond = p.expr()
		}
		p.expect(";")
		if !p.is(")") {
			n.Inc = p.expr()
		}
		p.expect(")")
		n.Then = p.loopBody()
		p.leaveScope()
	case p.consume("switch"):
		n.Kind = NSwitch
		p.expect("(")
		n.Cond = p.expr()
		p.expect(")")
		outer := p.sw
		p.sw = n
		p.breaks++
		n.Then = p.stmt()
		p.breaks--
		p.sw = outer
	case p.is("case") || p.is("default"):
		if p.sw == nil {
			p.fail(t, "case outside switch")
		}
		n.Kind = NCase
		if p.consume("default") {
			n.Default = true
		} else {
			p.next()
			n.Val = p.constExpr()
		}
		p.expect(":")
		p.sw.Cases = append(p.sw.Cases, n)
	case p.consume("return"):
		n.Kind = NReturn
		if !p.consume(";") {
			n.L = p.expr()
			p.expect(";")
		}
	case p.consume("break"):
		n.Kind = NBreak
		if p.breaks == 0 {
			p.fail(t, "break outside a loop or switch")
		}
		p.expect(";")
	case p.consume("continue"):
		n.Kind = NContinue
		if p.loops == 0 {
			p.fail(t, "continue outside a loop")
		}
		p.expect(";")
	default:
		n = exprStmt(p.expr())
		p.expect(";")
	}
	return n
}

func (p *parser) loopBody() *Node {
	p.loops++
	p.breaks++
	n := p.stmt()
	p.loops--
	p.breaks--
	return n
}

// ---- 式 ----

func (p *parser) constExpr() int64 {
	e := p.cond()
	v, label, err := p.checker.evalConst(e)
	if err != nil || label != "" {
		p.fail(e.Tok, "not a constant expression")
	}
	return v
}

func (p *parser) expr() *Node {
	n := p.assign()
	for p.is(",") {
		t := p.next()
		n = &Node{Kind: NComma, L: n, R: p.assign(), Tok: t}
	}
	return n
}

var assignOps = map[string]string{"=": "=", "+=": "+", "-=": "-", "*=": "*", "/=": "/", "%=": "%",
	"&=": "&", "|=": "|", "^=": "^", "<<=": "<<", ">>=": ">>"}

func (p *parser) assign() *Node {
	n := p.cond()
	if t := p.peek(); t.Kind == TPunct && assignOps[t.Text] != "" {
		p.next()
		return &Node{Kind: NAssign, Op: assignOps[t.Text], L: n, R: p.assign(), Tok: t}
	}
	return n
}

func (p *parser) cond() *Node {
	n := p.binary(0)
	if p.is("?") {
		t := p.next()
		c := &Node{Kind: NCond, Cond: n, Tok: t}
		c.Then = p.expr()
		p.expect(":")
		c.Else = p.cond()
		return c
	}
	return n
}

// 二項演算子の優先順位 (大きいほど強く結合する)。すべて左結合。
var precedence = map[string]int{
	"||": 1, "&&": 2, "|": 3, "^": 4, "&": 5, "==": 6, "!=": 6,
	"<": 7, "<=": 7, ">": 7, ">=": 7, "<<": 8, ">>": 8, "+": 9, "-": 9, "*": 10, "/": 10, "%": 10,
}

func (p *parser) binary(minPrec int) *Node {
	n := p.castExpr()
	for {
		t := p.peek()
		prec := precedence[t.Text]
		if t.Kind != TPunct || prec == 0 || prec <= minPrec {
			return n
		}
		p.next()
		n = &Node{Kind: NBinary, Op: t.Text, L: n, R: p.binary(prec), Tok: t}
	}
}

func (p *parser) castExpr() *Node {
	if p.is("(") && p.isTypeNameAt(p.pos+1) {
		t := p.next()
		ty := p.typeName()
		p.expect(")")
		return &Node{Kind: NCast, Ty: ty, L: p.castExpr(), Tok: t}
	}
	return p.unary()
}

func (p *parser) unary() *Node {
	t := p.peek()
	switch {
	case p.consume("+"):
		return p.castExpr()
	case p.consume("-"), p.consume("!"), p.consume("~"):
		return &Node{Kind: NUnary, Op: t.Text, L: p.castExpr(), Tok: t}
	case p.consume("&"):
		return &Node{Kind: NAddr, L: p.castExpr(), Tok: t}
	case p.consume("*"):
		return &Node{Kind: NDeref, L: p.castExpr(), Tok: t}
	case p.consume("++"), p.consume("--"):
		return &Node{Kind: NIncDec, Op: t.Text[:1], L: p.unary(), Tok: t}
	case p.consume("sizeof"):
		if p.is("(") && p.isTypeNameAt(p.pos+1) {
			p.next()
			ty := p.typeName()
			p.expect(")")
			return &Node{Kind: NNum, Val: int64(ty.Size), Ty: tyLong, Tok: t}
		}
		return &Node{Kind: NSizeof, L: p.unary(), Tok: t}
	}
	return p.postfix()
}

func (p *parser) postfix() *Node {
	n := p.primary()
	for {
		t := p.peek()
		switch {
		case p.consume("["): // a[i] は *(a + i)
			idx := p.expr()
			p.expect("]")
			n = &Node{Kind: NDeref, L: &Node{Kind: NBinary, Op: "+", L: n, R: idx, Tok: t}, Tok: t}
		case p.consume("("):
			call := &Node{Kind: NCall, L: n, Tok: t}
			for !p.consume(")") {
				if len(call.Body) > 0 {
					p.expect(",")
				}
				call.Body = append(call.Body, p.assign())
			}
			n = call
		case p.consume("."):
			n = &Node{Kind: NMember, L: n, Name: p.ident().Text, Tok: t}
		case p.consume("->"): // p->x は (*p).x
			n = &Node{Kind: NMember, L: &Node{Kind: NDeref, L: n, Tok: t}, Name: p.ident().Text, Tok: t}
		case p.consume("++"), p.consume("--"):
			n = &Node{Kind: NIncDec, Op: t.Text[:1], Post: true, L: n, Tok: t}
		default:
			return n
		}
	}
}

func (p *parser) primary() *Node {
	t := p.next()
	switch t.Kind {
	case TNum:
		n := &Node{Kind: NNum, Val: t.Val, Tok: t}
		if strings.ContainsAny(t.Text, "lL") { // 123L は long
			n.Ty = tyLong
		}
		return n
	case TStr:
		// 文字列リテラルは名前のない char 配列のグローバル変数にする
		v := &Obj{Name: fmt.Sprintf(".Lstr%d", p.nstr), Ty: arrayOf(tyChar, len(t.Text)+1), IsStatic: true, IsDef: true}
		p.nstr++
		v.InitBytes = append([]byte(t.Text), 0)
		p.prog.Globals = append(p.prog.Globals, v)
		return varNode(v, t)
	case TIdent:
		if t.Text == "__va_start" {
			p.expect("(")
			p.expect(")")
			if p.fn == nil || !p.fn.Ty.Variadic {
				p.fail(t, "__va_start outside a variadic function")
			}
			return &Node{Kind: NVaStart, Var: p.fn.VaArea, Tok: t}
		}
		e := p.lookup(t.Text)
		switch {
		case e == nil:
			p.fail(t, "undeclared identifier %s", t.Text)
		case e.enum != nil:
			return &Node{Kind: NNum, Val: *e.enum, Tok: t}
		case e.v != nil:
			return varNode(e.v, t)
		}
		p.fail(t, "unexpected type name")
	case TPunct:
		if t.Text == "(" {
			n := p.expr()
			p.expect(")")
			return n
		}
	}
	p.fail(t, "expected an expression")
	return nil
}
