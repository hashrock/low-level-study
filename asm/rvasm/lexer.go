package rvasm

// lexer.go — 1 行のアセンブリテキストをトークン列に分解する。
//
// トークンの種類:
//   tIdent  : 命令名・レジスタ名・ラベル・ディレクティブ (.text など)
//   tNum    : 数値リテラル (10進, 0x, 0b, 0o, 'c' 文字リテラル)
//   tStr    : "..." 文字列リテラル (エスケープ解決済み)
//   tPunct  : , ( ) : + - * % などの 1 文字記号
// コメントは # と // (行末まで)。

import (
	"fmt"
	"strconv"
	"strings"
)

type tokKind int

const (
	tIdent tokKind = iota
	tNum
	tStr
	tPunct
)

type token struct {
	kind tokKind
	text string // 識別子・記号・文字列の中身
	num  int64  // tNum の値
	col  int    // 行内の桁 (1 始まり、バイト単位)
}

func (t token) String() string {
	switch t.kind {
	case tNum:
		return strconv.FormatInt(t.num, 10)
	case tStr:
		return strconv.Quote(t.text)
	}
	return t.text
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '.' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// lexLine は 1 行をトークン化する。
func lexLine(s string) (toks []token, err error) {
	i := 0
	defer func() { // エラー位置 (桁) を付ける
		if err != nil {
			err = &colError{col: i + 1, err: err}
		}
	}()
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '#' || (c == '/' && i+1 < len(s) && s[i+1] == '/'):
			return toks, nil // コメント: 行末まで無視
		case c == ';':
			// GNU as と同様 ';' は文の区切りとして扱いたいが、
			// 簡単のため 1 行 1 文とし、';' 以降はエラーにする
			return nil, fmt.Errorf("';' is not supported (one statement per line)")
		case isIdentStart(c):
			j := i
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			toks = append(toks, token{col: i + 1, kind: tIdent, text: s[i:j]})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			v, err := parseNumber(s[i:j])
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{col: i + 1, kind: tNum, num: v, text: s[i:j]})
			i = j
		case c == '\'':
			// 文字リテラル 'a' または '\n'
			r, n, err := unescape(s[i+1:])
			if err != nil {
				return nil, err
			}
			if i+1+n >= len(s) || s[i+1+n] != '\'' {
				return nil, fmt.Errorf("unterminated char literal")
			}
			toks = append(toks, token{col: i + 1, kind: tNum, num: int64(r)})
			i += n + 2
		case c == '"':
			var sb strings.Builder
			j := i + 1
			for {
				if j >= len(s) {
					return nil, fmt.Errorf("unterminated string")
				}
				if s[j] == '"' {
					break
				}
				r, n, err := unescape(s[j:])
				if err != nil {
					return nil, err
				}
				sb.WriteByte(r)
				j += n
			}
			toks = append(toks, token{col: i + 1, kind: tStr, text: sb.String()})
			i = j + 1
		case strings.ContainsRune(",():+-*%<>~|&@", rune(c)):
			// "<<" / ">>" は 2 文字記号
			if (c == '<' || c == '>') && i+1 < len(s) && s[i+1] == c {
				toks = append(toks, token{col: i + 1, kind: tPunct, text: s[i : i+2]})
				i += 2
				continue
			}
			toks = append(toks, token{col: i + 1, kind: tPunct, text: string(c)})
			i++
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return toks, nil
}

func parseNumber(s string) (int64, error) {
	// 0x.. / 0b.. / 0o.. / 10進。64bit 符号なしの範囲まで許す (0xffff_ffff_ffff_ffff 等)
	u, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return int64(u), nil
}

// unescape は s の先頭 1 文字 (エスケープ込み) を解釈し、値と消費バイト数を返す。
func unescape(s string) (byte, int, error) {
	if len(s) == 0 {
		return 0, 0, fmt.Errorf("unexpected end of line")
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
	case '\\', '\'', '"':
		return s[1], 2, nil
	case 'x':
		j := 2
		for j < len(s) && j < 4 && strings.ContainsRune("0123456789abcdefABCDEF", rune(s[j])) {
			j++
		}
		v, err := strconv.ParseUint(s[2:j], 16, 8)
		if err != nil {
			return 0, 0, fmt.Errorf("bad \\x escape")
		}
		return byte(v), j, nil
	}
	return 0, 0, fmt.Errorf("unknown escape \\%c", s[1])
}

// colError は行内の桁つきエラー (lexLine が返す)。
type colError struct {
	col int
	err error
}

func (e *colError) Error() string { return e.err.Error() }
