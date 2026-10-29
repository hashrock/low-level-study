// Copied verbatim from ../lang/lexer.go (minilang). Do not edit here; keep in sync with lang.

package main

import (
	"fmt"
	"strings"
)

// Pos はソース上の位置 (1始まりの行:列)。
type Pos struct{ Line, Col int }

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// LangError は構文・コンパイル・実行時エラーを共通で表す。
type LangError struct {
	Pos   Pos
	Kind  string // "syntax error" / "compile error" / "runtime error"
	Msg   string
	Trace []string
	AtEOF bool // 入力の途中で終わった (REPL の複数行入力用)
}

func (e *LangError) Error() string {
	s := fmt.Sprintf("%s: %s: %s", e.Pos, e.Kind, e.Msg)
	for _, t := range e.Trace {
		s += "\n    at " + t
	}
	return s
}

// Token の Kind は "ident" "number" "string" "eof" か、キーワード/記号そのもの。
type Token struct {
	Kind string
	Lit  string
	Pos  Pos
}

var keywords = map[string]bool{
	"let": true, "const": true, "fn": true, "if": true, "else": true, "while": true,
	"for": true, "in": true, "return": true, "break": true, "continue": true,
	"true": true, "false": true, "nil": true,
}

var twoCharOps = map[string]bool{
	"==": true, "!=": true, "<=": true, ">=": true, "&&": true, "||": true,
	"=>": true, "+=": true, "-=": true, "*=": true, "/=": true,
}

func isDigit(r rune) bool  { return r >= '0' && r <= '9' }
func isLetter(r rune) bool { return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// Lex はソース全体をトークン列に変換する。
func Lex(src string) ([]Token, error) {
	rs := []rune(src)
	var toks []Token
	i, line, col := 0, 1, 1
	adv := func() rune {
		r := rs[i]
		i++
		if r == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
		return r
	}
	at := func(k int) rune {
		if i+k < len(rs) {
			return rs[i+k]
		}
		return 0
	}
	for {
		// 空白とコメント (// から行末まで) を読み飛ばす
		for i < len(rs) {
			if r := rs[i]; r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				adv()
			} else if r == '/' && at(1) == '/' {
				for i < len(rs) && rs[i] != '\n' {
					adv()
				}
			} else {
				break
			}
		}
		pos := Pos{line, col}
		if i >= len(rs) {
			return append(toks, Token{"eof", "", pos}), nil
		}
		r := rs[i]
		start := i
		switch {
		case isDigit(r):
			for i < len(rs) && isDigit(rs[i]) {
				adv()
			}
			if at(0) == '.' && isDigit(at(1)) {
				adv()
				for i < len(rs) && isDigit(rs[i]) {
					adv()
				}
			}
			toks = append(toks, Token{"number", string(rs[start:i]), pos})
		case isLetter(r):
			for i < len(rs) && (isLetter(rs[i]) || isDigit(rs[i])) {
				adv()
			}
			w := string(rs[start:i])
			kind := "ident"
			if keywords[w] {
				kind = w
			}
			toks = append(toks, Token{kind, w, pos})
		case r == '"':
			adv()
			var sb strings.Builder
			for {
				if i >= len(rs) || rs[i] == '\n' {
					return nil, &LangError{Pos: pos, Kind: "syntax error", Msg: "unterminated string"}
				}
				c := adv()
				if c == '"' {
					break
				}
				if c == '\\' && i < len(rs) {
					switch e := adv(); e {
					case 'n':
						c = '\n'
					case 't':
						c = '\t'
					case '"', '\\':
						c = e
					default:
						return nil, &LangError{Pos: Pos{line, col - 2}, Kind: "syntax error", Msg: fmt.Sprintf("unknown escape \\%c", e)}
					}
				}
				sb.WriteRune(c)
			}
			toks = append(toks, Token{"string", sb.String(), pos})
		default:
			if two := string(rs[i:min(i+2, len(rs))]); twoCharOps[two] {
				adv()
				adv()
				toks = append(toks, Token{two, two, pos})
			} else if strings.ContainsRune("+-*/%<>=!(){}[],:;.", r) {
				adv()
				toks = append(toks, Token{string(r), string(r), pos})
			} else {
				return nil, &LangError{Pos: pos, Kind: "syntax error", Msg: fmt.Sprintf("unexpected character %q", r)}
			}
		}
	}
}
