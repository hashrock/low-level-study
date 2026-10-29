package main

import (
	"fmt"
	"strings"
)

// File はソースファイル 1 つ。エラー表示とアセンブリ中のコメントに使う。
type File struct {
	Name  string
	Lines []string
}

type TokKind int

const (
	TIdent TokKind = iota
	TKeyword
	TPunct
	TNum
	TStr
	TEOF
)

type Token struct {
	Kind TokKind
	Text string // 識別子・記号の綴り / 文字列リテラルの中身 (エスケープ解釈済み)
	Val  int64
	File *File
	Line int
}

var keywords = map[string]bool{
	"void": true, "char": true, "short": true, "int": true, "long": true, "struct": true,
	"enum": true, "typedef": true, "sizeof": true, "if": true, "else": true, "while": true,
	"for": true, "do": true, "return": true, "break": true, "continue": true, "switch": true,
	"case": true, "default": true, "static": true, "extern": true, "const": true,
	"signed": true, "unsigned": true,
}

// 長いものから順に照合する
var puncts = []string{
	"<<=", ">>=", "...", "==", "!=", "<=", ">=", "&&", "||", "++", "--", "->",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<", ">>",
}

func errorAt(f *File, line int, format string, args ...any) error {
	return fmt.Errorf("%s:%d: error: %s", f.Name, line, fmt.Sprintf(format, args...))
}

// Tokenize はソース全体をトークン列にする。'#' で始まる行 (#include など) は丸ごと読み飛ばす。
// 隣り合う文字列リテラルは 1 つに連結する。__LINE__ は現在の行番号の数値になる。
func Tokenize(name, src string) ([]Token, error) {
	f := &File{Name: name, Lines: strings.Split(src, "\n")}
	var toks []Token
	line := 1
	bol := true // 行頭 (空白のみ) か
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			bol = true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '#' && bol:
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, errorAt(f, line, "unterminated comment")
			}
			line += strings.Count(src[i:i+2+end], "\n")
			i += end + 4
			continue
		}
		bol = false
		tok := Token{File: f, Line: line}
		switch {
		case isDigit(c):
			j := i
			for j < len(src) && (isAlnum(src[j])) {
				j++
			}
			v, err := parseInt(src[i:j])
			if err != nil {
				return nil, errorAt(f, line, "bad number %q", src[i:j])
			}
			tok.Kind, tok.Val, tok.Text = TNum, v, src[i:j]
			i = j
		case isAlpha(c):
			j := i
			for j < len(src) && isAlnum(src[j]) {
				j++
			}
			tok.Text = src[i:j]
			tok.Kind = TIdent
			if keywords[tok.Text] {
				tok.Kind = TKeyword
			} else if tok.Text == "__LINE__" {
				tok.Kind, tok.Val = TNum, int64(line)
			}
			i = j
		case c == '\'':
			b, n, err := unescape(src[i+1:])
			if err != nil || i+1+n >= len(src) || src[i+1+n] != '\'' {
				return nil, errorAt(f, line, "bad character literal")
			}
			tok.Kind, tok.Val = TNum, int64(int8(b)) // char は符号付き
			i += n + 2
		case c == '"':
			var sb strings.Builder
			j := i + 1
			for j < len(src) && src[j] != '"' {
				if src[j] == '\n' {
					return nil, errorAt(f, line, "unterminated string")
				}
				b, n, err := unescape(src[j:])
				if err != nil {
					return nil, errorAt(f, line, "%v", err)
				}
				sb.WriteByte(b)
				j += n
			}
			if j >= len(src) {
				return nil, errorAt(f, line, "unterminated string")
			}
			i = j + 1
			if n := len(toks); n > 0 && toks[n-1].Kind == TStr { // "ab" "cd" → "abcd"
				toks[n-1].Text += sb.String()
				continue
			}
			tok.Kind, tok.Text = TStr, sb.String()
		default:
			tok.Kind = TPunct
			tok.Text = string(c)
			for _, p := range puncts {
				if strings.HasPrefix(src[i:], p) {
					tok.Text = p
					break
				}
			}
			if !strings.Contains("+-*/%&|^~!<>=?:;,.()[]{}", tok.Text[:1]) {
				return nil, errorAt(f, line, "unexpected character %q", c)
			}
			i += len(tok.Text)
		}
		toks = append(toks, tok)
	}
	toks = append(toks, Token{Kind: TEOF, File: f, Line: line})
	return toks, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isAlpha(c) || isDigit(c) }

// parseInt は 10 進 / 0x 16 進 / 0 始まり 8 進 と、末尾の L/U を受け付ける。
func parseInt(s string) (int64, error) {
	s = strings.TrimRight(s, "lLuU")
	base := 10
	switch {
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		base, s = 16, s[2:]
	case len(s) > 1 && s[0] == '0':
		base, s = 8, s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var v uint64
	for _, c := range []byte(s) {
		d := strings.IndexByte("0123456789abcdef", c|0x20)
		if d < 0 || d >= base {
			return 0, fmt.Errorf("bad digit")
		}
		v = v*uint64(base) + uint64(d)
	}
	return int64(v), nil
}

func unescape(s string) (byte, int, error) {
	if s == "" {
		return 0, 0, fmt.Errorf("unexpected end of input")
	}
	if s[0] != '\\' {
		return s[0], 1, nil
	}
	if len(s) < 2 {
		return 0, 0, fmt.Errorf("bad escape")
	}
	switch s[1] {
	case 'n':
		return '\n', 2, nil
	case 't':
		return '\t', 2, nil
	case 'r':
		return '\r', 2, nil
	case '0':
		return 0, 2, nil
	case 'x':
		j := 2
		var v byte
		for j < len(s) && j < 4 && strings.IndexByte("0123456789abcdef", s[j]|0x20) >= 0 {
			v = v*16 + byte(strings.IndexByte("0123456789abcdef", s[j]|0x20))
			j++
		}
		return v, j, nil
	case '\\', '\'', '"', '?':
		return s[1], 2, nil
	}
	return 0, 0, fmt.Errorf("unknown escape \\%c", s[1])
}
