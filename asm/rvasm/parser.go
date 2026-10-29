package rvasm

// parser.go — トークン列を「文 (stmt)」にまとめ、オペランドを解釈する。
//
//   label:  label2:  mnemonic  op1, op2, op3   # comment
//
// オペランドは「トップレベルのカンマ」で分割したトークン列のまま保持し、
// 必要になった時点で reg / expr / mem として解釈する。
// (式の値は .equ の定義状況に依存するので、パスごとに評価し直す)

import (
	"fmt"
	"strings"
)

type stmt struct {
	line   int
	col    int    // 命令名 (なければ最初のラベル) の桁
	text   string // 元の行 (前後の空白を除いたもの)
	labels []string
	name   string    // 命令名 or ディレクティブ名 (小文字化済み) 。ラベルだけの行では ""
	args   [][]token // オペランド (カンマ区切り)
}

// parseSource はソース全体を文のリストにする。
func parseSource(src string) ([]stmt, error) {
	var out []stmt
	for i, line := range strings.Split(src, "\n") {
		toks, err := lexLine(line)
		if err != nil {
			col := 1
			if ce, ok := err.(*colError); ok {
				col = ce.col
			}
			return nil, &Error{Line: i + 1, Col: col, Msg: err.Error()}
		}
		st := stmt{line: i + 1, text: strings.TrimSpace(line)}
		if len(toks) > 0 {
			st.col = toks[0].col
		}
		// 先頭の "ident :" の並びはラベル
		for len(toks) >= 2 && toks[0].kind == tIdent && toks[1].kind == tPunct && toks[1].text == ":" {
			st.labels = append(st.labels, toks[0].text)
			toks = toks[2:]
		}
		if len(toks) > 0 {
			if toks[0].kind != tIdent {
				return nil, &Error{Line: i + 1, Col: toks[0].col, Msg: fmt.Sprintf("expected mnemonic, got %v", toks[0])}
			}
			st.col = toks[0].col
			st.name = strings.ToLower(toks[0].text)
			st.args = splitArgs(toks[1:])
		}
		if st.name != "" || len(st.labels) > 0 {
			out = append(out, st)
		}
	}
	return out, nil
}

// splitArgs は括弧の外にあるカンマでトークン列を分割する。
func splitArgs(toks []token) [][]token {
	if len(toks) == 0 {
		return nil
	}
	var args [][]token
	depth, start := 0, 0
	for i, t := range toks {
		if t.kind != tPunct {
			continue
		}
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				args = append(args, toks[start:i])
				start = i + 1
			}
		}
	}
	return append(args, toks[start:])
}

// ---- レジスタ ----

var abiNames = []string{
	"zero", "ra", "sp", "gp", "tp", "t0", "t1", "t2",
	"s0", "s1", "a0", "a1", "a2", "a3", "a4", "a5",
	"a6", "a7", "s2", "s3", "s4", "s5", "s6", "s7",
	"s8", "s9", "s10", "s11", "t3", "t4", "t5", "t6",
}

var regMap = func() map[string]int {
	m := map[string]int{"fp": 8}
	for i, n := range abiNames {
		m[n] = i
		m[fmt.Sprintf("x%d", i)] = i
	}
	return m
}()

func parseReg(toks []token) (int, error) {
	if len(toks) == 1 && toks[0].kind == tIdent {
		if r, ok := regMap[strings.ToLower(toks[0].text)]; ok {
			return r, nil
		}
	}
	return 0, fmt.Errorf("expected register, got %q", joinToks(toks))
}

func joinToks(toks []token) string {
	var parts []string
	for _, t := range toks {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, " ")
}

// ---- 式 ----

// expr は評価済みの式。値は「シンボル + 定数」の形に限る。
// mod は %hi(...) などの修飾子 ("" / "hi" / "lo" / "pcrel_hi" / "pcrel_lo")。
type expr struct {
	sym string
	add int64
	mod string
}

// exprParser は再帰下降で式を評価する。
// 優先順位 (低→高):  |  &  << >>  + -  *  単項(- ~)
type exprParser struct {
	toks   []token
	pos    int
	lookup resolver
}

// resolver はシンボル名を引く関数。
// sec == "" なら .equ 定数 (val がその値)、sec != "" ならそのセクション内のラベル。
// "." は現在位置を表す。
type resolver func(name string) (sec string, val int64, ok bool)

func parseExpr(toks []token, lookup resolver) (expr, error) {
	if len(toks) == 0 {
		return expr{}, fmt.Errorf("missing expression")
	}
	// %hi(sym) などの修飾子
	if toks[0].kind == tPunct && toks[0].text == "%" {
		if len(toks) < 4 || toks[1].kind != tIdent || toks[2].text != "(" || toks[len(toks)-1].text != ")" {
			return expr{}, fmt.Errorf("bad relocation modifier %q", joinToks(toks))
		}
		mod := toks[1].text
		switch mod {
		case "hi", "lo", "pcrel_hi", "pcrel_lo":
		default:
			return expr{}, fmt.Errorf("unknown modifier %%%s", mod)
		}
		e, err := parseExpr(toks[3:len(toks)-1], lookup)
		e.mod = mod
		return e, err
	}
	p := &exprParser{toks: toks, lookup: lookup}
	e, err := p.binary(0)
	if err != nil {
		return expr{}, err
	}
	if p.pos != len(toks) {
		return expr{}, fmt.Errorf("unexpected %q in expression", toks[p.pos].String())
	}
	return e, nil
}

var binPrec = map[string]int{"|": 1, "&": 2, "<<": 3, ">>": 3, "+": 4, "-": 4, "*": 5}

func (p *exprParser) binary(minPrec int) (expr, error) {
	lhs, err := p.unary()
	if err != nil {
		return lhs, err
	}
	for p.pos < len(p.toks) {
		op := p.toks[p.pos]
		prec, ok := binPrec[op.text]
		if op.kind != tPunct || !ok || prec <= minPrec {
			break
		}
		p.pos++
		rhs, err := p.binary(prec)
		if err != nil {
			return lhs, err
		}
		if lhs, err = p.applyOp(op.text, lhs, rhs); err != nil {
			return lhs, err
		}
	}
	return lhs, nil
}

func (p *exprParser) applyOp(op string, a, b expr) (expr, error) {
	if op == "-" && a.sym != "" && b.sym != "" {
		// ラベル同士の差 (例: . - msg) は、同じセクション内で両方定義済みなら定数になる
		s1, v1, ok1 := p.lookup(a.sym)
		s2, v2, ok2 := p.lookup(b.sym)
		if !ok1 || !ok2 || s1 != s2 {
			return expr{}, fmt.Errorf("%s - %s is not a constant (both must be defined earlier in the same section)", a.sym, b.sym)
		}
		return expr{add: v1 + a.add - v2 - b.add}, nil
	}
	switch {
	case op == "+" && (a.sym == "" || b.sym == ""):
		return expr{sym: a.sym + b.sym, add: a.add + b.add}, nil
	case op == "-" && b.sym == "":
		return expr{sym: a.sym, add: a.add - b.add}, nil
	case a.sym != "" || b.sym != "":
		return expr{}, fmt.Errorf("unsupported operation %q on symbol", op)
	}
	x, y := a.add, b.add
	switch op {
	case "-":
		x -= y
	case "*":
		x *= y
	case "<<":
		x <<= uint(y)
	case ">>":
		x >>= uint(y)
	case "&":
		x &= y
	case "|":
		x |= y
	}
	return expr{add: x}, nil
}

func (p *exprParser) unary() (expr, error) {
	if p.pos >= len(p.toks) {
		return expr{}, fmt.Errorf("unexpected end of expression")
	}
	t := p.toks[p.pos]
	p.pos++
	switch {
	case t.kind == tNum:
		return expr{add: t.num}, nil
	case t.kind == tIdent:
		if sec, v, ok := p.lookup(t.text); ok && sec == "" {
			return expr{add: v}, nil
		}
		return expr{sym: t.text}, nil
	case t.text == "-" || t.text == "~":
		e, err := p.unary()
		if err != nil || e.sym != "" {
			return e, fmt.Errorf("cannot negate symbol")
		}
		if t.text == "-" {
			return expr{add: -e.add}, nil
		}
		return expr{add: ^e.add}, nil
	case t.text == "(":
		e, err := p.binary(0)
		if err != nil {
			return e, err
		}
		if p.pos >= len(p.toks) || p.toks[p.pos].text != ")" {
			return e, fmt.Errorf("missing ')'")
		}
		p.pos++
		return e, nil
	}
	return expr{}, fmt.Errorf("unexpected %q in expression", t.String())
}

// parseMem は "offset(reg)" または "(reg)" を解釈する。
func parseMem(toks []token, lookup resolver) (expr, int, error) {
	n := len(toks)
	if n < 3 || toks[n-1].text != ")" || toks[n-3].text != "(" {
		return expr{}, 0, fmt.Errorf("expected offset(reg), got %q", joinToks(toks))
	}
	r, err := parseReg(toks[n-2 : n-1])
	if err != nil {
		return expr{}, 0, err
	}
	if n == 3 {
		return expr{}, r, nil
	}
	e, err := parseExpr(toks[:n-3], lookup)
	return e, r, err
}
