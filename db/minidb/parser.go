package minidb

import (
	"fmt"
	"strconv"
	"strings"
)

// 構文解析: トークン列を AST に変換する再帰下降パーサ。

type Parser struct {
	toks []Token
	pos  int
}

// Parse は ; 区切りの複数の文を解析する。
func Parse(src string) ([]Statement, error) {
	stmts, _, err := parseWithSource(src)
	return stmts, err
}

// parseWithSource は Parse と同じだが、各文の元の SQL テキストも返す (統計表示用)。
func parseWithSource(src string) ([]Statement, []string, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, nil, err
	}
	p := &Parser{toks: toks}
	var stmts []Statement
	var texts []string
	for {
		for p.acceptSym(";") {
		}
		if p.peek().Kind == TokEOF {
			return stmts, texts, nil
		}
		start := p.peek().Pos
		s, err := p.statement()
		if err != nil {
			return nil, nil, err
		}
		stmts = append(stmts, s)
		texts = append(texts, strings.TrimSpace(src[start:p.peek().Pos]))
		if !p.acceptSym(";") && p.peek().Kind != TokEOF {
			return nil, nil, p.errorf("expected ';'")
		}
	}
}

// ---- トークン操作の小道具 ----

func (p *Parser) peek() Token { return p.toks[p.pos] }
func (p *Parser) next() Token {
	t := p.toks[p.pos]
	if t.Kind != TokEOF {
		p.pos++
	}
	return t
}

func (p *Parser) errorf(format string, args ...any) error {
	t := p.peek()
	near := t.Text
	if t.Kind == TokEOF {
		near = "end of input"
	}
	return fmt.Errorf("parse error at %d near %q: %s", t.Pos, near, fmt.Sprintf(format, args...))
}

func (p *Parser) isKeyword(kw string) bool {
	t := p.peek()
	return t.Kind == TokKeyword && t.Text == kw
}

func (p *Parser) acceptKeyword(kw string) bool {
	if p.isKeyword(kw) {
		p.pos++
		return true
	}
	return false
}

func (p *Parser) expectKeyword(kw string) error {
	if !p.acceptKeyword(kw) {
		return p.errorf("expected %s", kw)
	}
	return nil
}

func (p *Parser) acceptSym(s string) bool {
	t := p.peek()
	if t.Kind == TokSymbol && t.Text == s {
		p.pos++
		return true
	}
	return false
}

func (p *Parser) expectSym(s string) error {
	if !p.acceptSym(s) {
		return p.errorf("expected '%s'", s)
	}
	return nil
}

func (p *Parser) expectIdent() (string, error) {
	t := p.peek()
	if t.Kind != TokIdent {
		return "", p.errorf("expected identifier")
	}
	p.pos++
	return t.Text, nil
}

// ---- 文 ----

func (p *Parser) statement() (Statement, error) {
	switch {
	case p.acceptKeyword("CREATE"):
		return p.createTable()
	case p.acceptKeyword("INSERT"):
		return p.insert()
	case p.acceptKeyword("SELECT"):
		return p.selectStmt()
	case p.acceptKeyword("UPDATE"):
		return p.update()
	case p.acceptKeyword("DELETE"):
		return p.deleteStmt()
	}
	return nil, p.errorf("unknown statement")
}

// CREATE TABLE name (col type [PRIMARY KEY], ...)
func (p *Parser) createTable() (Statement, error) {
	if err := p.expectKeyword("TABLE"); err != nil {
		return nil, err
	}
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if err := p.expectSym("("); err != nil {
		return nil, err
	}
	s := &CreateTableStmt{Name: name}
	for {
		col, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		c := Column{Name: col}
		switch {
		case p.acceptKeyword("INTEGER"), p.acceptKeyword("INT"):
			c.Type = TypeInt
		case p.acceptKeyword("TEXT"):
			c.Type = TypeText
		default:
			return nil, p.errorf("expected column type INTEGER or TEXT")
		}
		if p.acceptKeyword("PRIMARY") {
			if err := p.expectKeyword("KEY"); err != nil {
				return nil, err
			}
			c.PrimaryKey = true
		}
		s.Columns = append(s.Columns, c)
		if !p.acceptSym(",") {
			break
		}
	}
	return s, p.expectSym(")")
}

// INSERT INTO t [(a, b)] VALUES (1, 'x'), (2, 'y')
func (p *Parser) insert() (Statement, error) {
	if err := p.expectKeyword("INTO"); err != nil {
		return nil, err
	}
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	s := &InsertStmt{Table: name}
	if p.acceptSym("(") {
		for {
			col, err := p.expectIdent()
			if err != nil {
				return nil, err
			}
			s.Columns = append(s.Columns, col)
			if !p.acceptSym(",") {
				break
			}
		}
		if err := p.expectSym(")"); err != nil {
			return nil, err
		}
	}
	if err := p.expectKeyword("VALUES"); err != nil {
		return nil, err
	}
	for {
		if err := p.expectSym("("); err != nil {
			return nil, err
		}
		var row []Expr
		for {
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			row = append(row, e)
			if !p.acceptSym(",") {
				break
			}
		}
		if err := p.expectSym(")"); err != nil {
			return nil, err
		}
		s.Rows = append(s.Rows, row)
		if !p.acceptSym(",") {
			return s, nil
		}
	}
}

// SELECT */cols FROM t [JOIN u ON ...] [WHERE ...] [ORDER BY c [DESC]] [LIMIT n]
func (p *Parser) selectStmt() (Statement, error) {
	s := &SelectStmt{Limit: -1}
	if p.acceptSym("*") {
		s.Star = true
	} else {
		for {
			c, err := p.columnRef()
			if err != nil {
				return nil, err
			}
			s.Columns = append(s.Columns, c)
			if !p.acceptSym(",") {
				break
			}
		}
	}
	if err := p.expectKeyword("FROM"); err != nil {
		return nil, err
	}
	var err error
	if s.From, err = p.expectIdent(); err != nil {
		return nil, err
	}
	p.acceptKeyword("INNER")
	if p.acceptKeyword("JOIN") {
		j := &JoinClause{}
		if j.Table, err = p.expectIdent(); err != nil {
			return nil, err
		}
		if err := p.expectKeyword("ON"); err != nil {
			return nil, err
		}
		if j.On, err = p.expr(); err != nil {
			return nil, err
		}
		s.Join = j
	}
	if s.Where, err = p.optWhere(); err != nil {
		return nil, err
	}
	if p.acceptKeyword("ORDER") {
		if err := p.expectKeyword("BY"); err != nil {
			return nil, err
		}
		c, err := p.columnRef()
		if err != nil {
			return nil, err
		}
		s.OrderBy = &OrderBy{Column: c}
		if p.acceptKeyword("DESC") {
			s.OrderBy.Desc = true
		} else {
			p.acceptKeyword("ASC")
		}
	}
	if p.acceptKeyword("LIMIT") {
		t := p.next()
		if t.Kind != TokInt {
			return nil, p.errorf("expected integer after LIMIT")
		}
		s.Limit, _ = strconv.Atoi(t.Text)
	}
	return s, nil
}

// UPDATE t SET a = expr, ... [WHERE ...]
func (p *Parser) update() (Statement, error) {
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if err := p.expectKeyword("SET"); err != nil {
		return nil, err
	}
	s := &UpdateStmt{Table: name}
	for {
		col, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		if err := p.expectSym("="); err != nil {
			return nil, err
		}
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		s.Sets = append(s.Sets, Assignment{col, e})
		if !p.acceptSym(",") {
			break
		}
	}
	s.Where, err = p.optWhere()
	return s, err
}

// DELETE FROM t [WHERE ...]
func (p *Parser) deleteStmt() (Statement, error) {
	if err := p.expectKeyword("FROM"); err != nil {
		return nil, err
	}
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	s := &DeleteStmt{Table: name}
	s.Where, err = p.optWhere()
	return s, err
}

func (p *Parser) optWhere() (Expr, error) {
	if p.acceptKeyword("WHERE") {
		return p.expr()
	}
	return nil, nil
}

// ---- 式 (優先順位: OR < AND < 比較) ----

func (p *Parser) expr() (Expr, error) { return p.orExpr() }

func (p *Parser) orExpr() (Expr, error) {
	left, err := p.andExpr()
	for err == nil && p.acceptKeyword("OR") {
		var right Expr
		if right, err = p.andExpr(); err == nil {
			left = &BinaryExpr{"OR", left, right}
		}
	}
	return left, err
}

func (p *Parser) andExpr() (Expr, error) {
	left, err := p.comparison()
	for err == nil && p.acceptKeyword("AND") {
		var right Expr
		if right, err = p.comparison(); err == nil {
			left = &BinaryExpr{"AND", left, right}
		}
	}
	return left, err
}

func (p *Parser) comparison() (Expr, error) {
	left, err := p.primary()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"=", "!=", "<=", ">=", "<", ">"} {
		if p.acceptSym(op) {
			right, err := p.primary()
			if err != nil {
				return nil, err
			}
			return &BinaryExpr{op, left, right}, nil
		}
	}
	return left, nil
}

func (p *Parser) primary() (Expr, error) {
	t := p.peek()
	switch {
	case t.Kind == TokInt || (t.Kind == TokSymbol && t.Text == "-"):
		neg := p.acceptSym("-")
		t = p.next()
		if t.Kind != TokInt {
			return nil, p.errorf("expected integer")
		}
		n, err := strconv.ParseInt(t.Text, 10, 64)
		if err != nil {
			return nil, err
		}
		if neg {
			n = -n
		}
		return &Literal{IntValue(n)}, nil
	case t.Kind == TokString:
		p.next()
		return &Literal{TextValue(t.Text)}, nil
	case p.acceptKeyword("NULL"):
		return &Literal{Value{}}, nil
	case t.Kind == TokIdent:
		c, err := p.columnRef()
		return &c, err
	case p.acceptSym("("):
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		return e, p.expectSym(")")
	}
	return nil, p.errorf("expected expression")
}

// col または table.col
func (p *Parser) columnRef() (ColumnRef, error) {
	name, err := p.expectIdent()
	if err != nil {
		return ColumnRef{}, err
	}
	if p.acceptSym(".") {
		col, err := p.expectIdent()
		return ColumnRef{Table: name, Name: col}, err
	}
	return ColumnRef{Name: name}, nil
}
