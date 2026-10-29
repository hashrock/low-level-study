// Package rvcc は C のサブセットを RISC-V 64 (RV64IM) アセンブリにするコンパイラ。
//
// ファイルシステムにもサブプロセスにも触らない (ブラウザ上の WebAssembly からもそのまま使える)。
//
//	out, err := rvcc.Compile("fib.c", src, 2)     // .c → アセンブリ (+ アセンブリ行 → C の行の対応)
//	exe, err := rvcc.Build([]rvcc.Source{{Name: "fib.c", Text: src}}, 2)  // libc と一緒に ELF まで
//	outs, err := rvcc.CompileAll(files, 2)       // crt0.s + libc.s + 各 .s (rvcc.AsmSources(outs) を rvasm.Build へ)
//
// エラーは *Error (ファイル名・行・桁つき) で返る。
package rvcc

import (
	"embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"rvasm/rvasm"
)

//go:embed libc/libc.h libc/libc.c libc/crt0.s
var libcFS embed.FS

// Source は C のソース 1 つ (ファイル名と中身)。
type Source struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Output は 1 つの翻訳単位のコンパイル結果。
type Output struct {
	Source string `json:"source"` // 元の .c の名前
	Name   string `json:"name"`   // アセンブリの名前 (fib.c → fib.s)
	Asm    string `json:"asm"`
	// Lines[k] はアセンブリの k+1 行目を生んだ C の行番号 (1 始まり、0 は対応なし)。
	// 文の先頭に出る "# 12: ..." コメントの行から次のコメントまでがその行、
	// 関数の prologue / epilogue は関数定義の行になる。
	Lines []int `json:"lines"`
}

// Libc は埋め込まれた libc のソースを返す。
// crt0.s はアセンブリ、libc.c は rvcc でコンパイルする C、libc.h は全 .c の先頭に暗黙に読み込まれる宣言。
func Libc() (crt0, libcC, libcH string) {
	a, _ := libcFS.ReadFile("libc/crt0.s")
	b, _ := libcFS.ReadFile("libc/libc.c")
	c, _ := libcFS.ReadFile("libc/libc.h")
	return string(a), string(b), string(c)
}

// Compile は 1 つの .c (翻訳単位) をアセンブリにする。opt は最適化レベル 0..2。
// 先頭に libc.h (プロトタイプ宣言) を暗黙に読み込む: プリプロセッサが無いので #include は無視される。
func Compile(name, src string, opt int) (*Output, error) {
	_, _, hdr := Libc()
	pre, err := Tokenize("libc.h", hdr)
	if err != nil {
		return nil, err
	}
	toks, err := Tokenize(name, src)
	if err != nil {
		return nil, err
	}
	prog, err := Parse(append(pre[:len(pre)-1], toks...))
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("# %s (rvcc -O%d)\n", name, opt) + Generate(prog, opt)
	fnLine := map[string]int{}
	for _, f := range prog.Globals {
		if f.Ty.Kind == TyFunc && f.IsDef {
			fnLine[f.Name] = f.Line
		}
	}
	return &Output{Source: name, Name: strings.TrimSuffix(name, ".c") + ".s", Asm: asm, Lines: lineMap(asm, fnLine)}, nil
}

var (
	reFnHeader = regexp.MustCompile(`^# ---- (\S+) ----$`)
	reLineCmt  = regexp.MustCompile(`^\t# (\d+): `)
	reRetLabel = regexp.MustCompile(`^\.Lret_(\S+):$`)
)

// lineMap は codegen が出すコメントからアセンブリ行 → C の行の対応を作る。
func lineMap(asm string, fnLine map[string]int) []int {
	lines := strings.Split(strings.TrimSuffix(asm, "\n"), "\n")
	out := make([]int, len(lines))
	cur := 0
	for k, l := range lines {
		if m := reFnHeader.FindStringSubmatch(l); m != nil {
			cur = fnLine[m[1]]
		} else if m := reRetLabel.FindStringSubmatch(l); m != nil {
			cur = fnLine[m[1]]
		} else if m := reLineCmt.FindStringSubmatch(l); m != nil {
			cur, _ = strconv.Atoi(m[1])
		} else if l == "\t.data" || l == "\t.bss" || l == "\t.rodata" || l == "\t.text" {
			cur = 0
		}
		if l != "" {
			out[k] = cur
		}
	}
	return out
}

// CompileAll は libc.c と各ソースをコンパイルし、crt0.s を加えた「リンクに必要な全アセンブリ」を返す。
// 返り値の順序は crt0.s, libc.s, 各ソースの .s。crt0.s の Lines はすべて 0。
func CompileAll(srcs []Source, opt int) ([]*Output, error) {
	crt0, libcC, _ := Libc()
	outs := []*Output{{Source: "crt0.s", Name: "crt0.s", Asm: crt0, Lines: make([]int, len(strings.Split(strings.TrimSuffix(crt0, "\n"), "\n")))}}
	lib, err := Compile("libc.c", libcC, opt)
	if err != nil {
		return nil, fmt.Errorf("libc: %w", err)
	}
	outs = append(outs, lib)
	for _, s := range srcs {
		o, err := Compile(s.Name, s.Text, opt)
		if err != nil {
			return nil, err
		}
		outs = append(outs, o)
	}
	return outs, nil
}

// AsmSources は CompileAll の結果を rvasm.Build に渡せる形にする。
func AsmSources(outs []*Output) []rvasm.Source {
	var srcs []rvasm.Source
	for _, o := range outs {
		srcs = append(srcs, rvasm.Source{Name: o.Name, Text: o.Asm})
	}
	return srcs
}

// Build は C のソースを libc と一緒にコンパイル・アセンブル・リンクし、静的 ELF 実行ファイルを返す。
// すべてメモリ上で行う。
func Build(srcs []Source, opt int) ([]byte, error) {
	outs, err := CompileAll(srcs, opt)
	if err != nil {
		return nil, err
	}
	return rvasm.Build(AsmSources(outs)...)
}
