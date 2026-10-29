package minidb

import (
	"fmt"
	"strings"
)

// 字句解析: SQL 文字列をトークン列に分解する。

type TokenKind int

const (
	TokEOF TokenKind = iota
	TokIdent
	TokKeyword
	TokInt
	TokString
	TokSymbol
)

type Token struct {
	Kind TokenKind
	Text string // キーワードは大文字、識別子は小文字に正規化
	Pos  int
}

var keywords = map[string]bool{
	"CREATE": true, "TABLE": true, "INSERT": true, "INTO": true, "VALUES": true,
	"SELECT": true, "FROM": true, "WHERE": true, "AND": true, "OR": true,
	"ORDER": true, "BY": true, "ASC": true, "DESC": true, "LIMIT": true,
	"UPDATE": true, "SET": true, "DELETE": true, "PRIMARY": true, "KEY": true,
	"INTEGER": true, "INT": true, "TEXT": true, "NULL": true,
	"JOIN": true, "INNER": true, "ON": true,
}

func isLetter(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

func Lex(src string) ([]Token, error) {
	var toks []Token
	i := 0
	for i < len(src) {
		c := src[i]
		start := i
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-': // -- コメント
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case isLetter(c):
			for i < len(src) && (isLetter(src[i]) || isDigit(src[i])) {
				i++
			}
			word := src[start:i]
			if up := strings.ToUpper(word); keywords[up] {
				toks = append(toks, Token{TokKeyword, up, start})
			} else {
				toks = append(toks, Token{TokIdent, strings.ToLower(word), start})
			}
		case isDigit(c):
			for i < len(src) && isDigit(src[i]) {
				i++
			}
			toks = append(toks, Token{TokInt, src[start:i], start})
		case c == '\'':
			// 文字列リテラル。'' はエスケープされた ' 。
			var sb strings.Builder
			i++
			for {
				if i >= len(src) {
					return nil, fmt.Errorf("unterminated string at %d", start)
				}
				if src[i] == '\'' {
					if i+1 < len(src) && src[i+1] == '\'' {
						sb.WriteByte('\'')
						i += 2
						continue
					}
					i++
					break
				}
				sb.WriteByte(src[i])
				i++
			}
			toks = append(toks, Token{TokString, sb.String(), start})
		default:
			if i+1 < len(src) {
				two := src[i : i+2]
				if two == "<>" {
					two = "!="
				}
				if two == "!=" || two == "<=" || two == ">=" {
					toks = append(toks, Token{TokSymbol, two, start})
					i += 2
					continue
				}
			}
			if strings.IndexByte("(),;*=<>.-", c) < 0 {
				return nil, fmt.Errorf("unexpected character %q at %d", c, i)
			}
			toks = append(toks, Token{TokSymbol, string(c), start})
			i++
		}
	}
	return append(toks, Token{TokEOF, "", len(src)}), nil
}
