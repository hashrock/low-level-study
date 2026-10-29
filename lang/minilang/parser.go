package minilang

import (
	"fmt"
	"strconv"
)

// 優先順位 (小さいほど弱く結合する)
const (
	precLowest = iota
	precAssign // = += -= *= /=   (右結合)
	precOr     // ||
	precAnd    // &&
	precEq     // == !=
	precCmp    // < <= > >=
	precSum    // + -
	precMul    // * / %
	precUnary  // -x !x
	precCall   // f() a[i] a.b
)

var infixPrec = map[string]int{
	"=": precAssign, "+=": precAssign, "-=": precAssign, "*=": precAssign, "/=": precAssign,
	"||": precOr, "&&": precAnd, "==": precEq, "!=": precEq,
	"<": precCmp, "<=": precCmp, ">": precCmp, ">=": precCmp,
	"+": precSum, "-": precSum, "*": precMul, "/": precMul, "%": precMul,
	"(": precCall, "[": precCall, ".": precCall,
}

// Parser はトークン列から AST を作る Pratt パーサ。
type Parser struct {
	toks []Token
	i    int
}

func Parse(src string) (prog []Stmt, err error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	p := &Parser{toks: toks}
	defer func() {
		if r := recover(); r != nil {
			le, ok := r.(*LangError)
			if !ok {
				panic(r)
			}
			err = le
		}
	}()
	for p.peek().Kind != "eof" {
		prog = append(prog, p.stmt())
	}
	return prog, nil
}

func (p *Parser) peek() Token { return p.toks[p.i] }
func (p *Parser) next() Token {
	t := p.toks[p.i]
	if t.Kind != "eof" {
		p.i++
	}
	return t
}
func (p *Parser) accept(kind string) bool {
	if p.peek().Kind == kind {
		p.next()
		return true
	}
	return false
}
func (p *Parser) fail(t Token, format string, args ...any) {
	panic(&LangError{Pos: t.Pos, Kind: "syntax error", Msg: fmt.Sprintf(format, args...), AtEOF: t.Kind == "eof"})
}
func (p *Parser) expect(kind string) Token {
	t := p.next()
	if t.Kind != kind {
		p.fail(t, "expected %q but found %s", kind, describe(t))
	}
	return t
}
func describe(t Token) string {
	if t.Kind == "eof" {
		return "end of input"
	}
	return fmt.Sprintf("%q", t.Lit)
}

// ---- 文 ----

func (p *Parser) stmt() Stmt {
	t := p.peek()
	b := base{t.Pos}
	var s Stmt
	switch t.Kind {
	case "let", "const":
		p.next()
		name := p.expect("ident")
		var val Expr = &NilLit{b}
		if p.accept("=") {
			val = p.expr(precLowest)
		} else if t.Kind == "const" {
			p.fail(name, "const %s needs an initial value", name.Lit)
		}
		s = &LetStmt{b, name.Lit, t.Kind == "const", val, false}
	case "fn":
		if p.toks[p.i+1].Kind == "ident" { // fn name(...) {...} は宣言文
			p.next()
			name := p.next()
			s = &LetStmt{b, name.Lit, true, p.funcRest(t.Pos, name.Lit), true}
		} else {
			s = &ExprStmt{b, p.expr(precLowest)}
		}
	case "if":
		s = p.ifStmt()
	case "while":
		p.next()
		p.expect("(")
		cond := p.expr(precLowest)
		p.expect(")")
		s = &WhileStmt{b, cond, p.block()}
	case "for":
		p.next()
		p.expect("(")
		v := p.expect("ident")
		p.expect("in")
		it := p.expr(precLowest)
		p.expect(")")
		s = &ForInStmt{b, v.Lit, it, p.block()}
	case "return":
		p.next()
		var v Expr
		if k := p.peek().Kind; k != ";" && k != "}" && k != "eof" {
			v = p.expr(precLowest)
		}
		s = &ReturnStmt{b, v}
	case "break":
		p.next()
		s = &BreakStmt{b}
	case "continue":
		p.next()
		s = &ContinueStmt{b}
	case "{":
		s = &BlockStmt{b, p.block()}
	default:
		s = &ExprStmt{b, p.expr(precLowest)}
	}
	p.accept(";") // セミコロンは省略可能
	return s
}

func (p *Parser) ifStmt() Stmt {
	t := p.expect("if")
	p.expect("(")
	cond := p.expr(precLowest)
	p.expect(")")
	s := &IfStmt{base{t.Pos}, cond, p.block(), nil}
	if p.accept("else") {
		if p.peek().Kind == "if" {
			s.Else = p.ifStmt()
		} else {
			s.Else = &BlockStmt{base{p.peek().Pos}, p.block()}
		}
	}
	return s
}

func (p *Parser) block() []Stmt {
	p.expect("{")
	body := []Stmt{}
	for p.peek().Kind != "}" {
		if p.peek().Kind == "eof" {
			p.fail(p.peek(), "expected \"}\" but found end of input")
		}
		body = append(body, p.stmt())
	}
	p.next()
	return body
}

// funcRest は "(" params ")" { body } または => expr を読む。
func (p *Parser) funcRest(pos Pos, name string) *FuncLit {
	p.expect("(")
	var params []string
	for p.peek().Kind != ")" {
		params = append(params, p.expect("ident").Lit)
		if !p.accept(",") {
			break
		}
	}
	p.expect(")")
	var body []Stmt
	if arrow := p.peek(); p.accept("=>") {
		e := p.expr(precLowest)
		body = []Stmt{&ReturnStmt{base{arrow.Pos}, e}}
	} else {
		body = p.block()
	}
	return &FuncLit{base{pos}, name, params, body}
}

// ---- 式 (Pratt) ----

func (p *Parser) expr(prec int) Expr {
	left := p.prefix()
	for {
		t := p.peek()
		pr, ok := infixPrec[t.Kind]
		if !ok || pr <= prec {
			return left
		}
		p.next()
		left = p.infix(left, t, pr)
	}
}

func (p *Parser) prefix() Expr {
	t := p.next()
	b := base{t.Pos}
	switch t.Kind {
	case "number":
		v, _ := strconv.ParseFloat(t.Lit, 64)
		return &NumberLit{b, v}
	case "string":
		return &StringLit{b, t.Lit}
	case "true", "false":
		return &BoolLit{b, t.Kind == "true"}
	case "nil":
		return &NilLit{b}
	case "ident":
		return &Ident{b, t.Lit}
	case "(":
		e := p.expr(precLowest)
		p.expect(")")
		return e
	case "[":
		arr := &ArrayLit{b, nil}
		for p.peek().Kind != "]" {
			arr.Elems = append(arr.Elems, p.expr(precLowest))
			if !p.accept(",") {
				break
			}
		}
		p.expect("]")
		return arr
	case "{":
		m := &MapLit{b, nil, nil}
		for p.peek().Kind != "}" {
			k := p.next()
			if k.Kind != "ident" && k.Kind != "string" {
				p.fail(k, "map key must be a name or string, found %s", describe(k))
			}
			p.expect(":")
			m.Keys = append(m.Keys, k.Lit)
			m.Vals = append(m.Vals, p.expr(precLowest))
			if !p.accept(",") {
				break
			}
		}
		p.expect("}")
		return m
	case "fn":
		return p.funcRest(t.Pos, "")
	case "-", "!":
		return &Unary{b, t.Kind, p.expr(precUnary)}
	}
	p.fail(t, "unexpected %s", describe(t))
	return nil
}

func (p *Parser) infix(left Expr, t Token, prec int) Expr {
	b := base{t.Pos}
	switch t.Kind {
	case "(":
		call := &Call{b, left, nil}
		for p.peek().Kind != ")" {
			call.Args = append(call.Args, p.expr(precLowest))
			if !p.accept(",") {
				break
			}
		}
		p.expect(")")
		return call
	case "[":
		key := p.expr(precLowest)
		p.expect("]")
		return &Index{b, left, key}
	case ".":
		name := p.expect("ident")
		return &Index{b, left, &StringLit{base{name.Pos}, name.Lit}}
	case "=", "+=", "-=", "*=", "/=":
		switch left.(type) {
		case *Ident, *Index:
		default:
			p.fail(t, "invalid assignment target")
		}
		val := p.expr(precAssign - 1) // 右結合
		if t.Kind != "=" {            // a += b は a = a + b に展開
			val = &Binary{b, t.Kind[:1], left, val}
		}
		return &Assign{b, left, val}
	}
	return &Binary{b, t.Kind, left, p.expr(prec)}
}
