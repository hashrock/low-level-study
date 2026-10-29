// Package minicc は minilang (動的型の JS 風言語) を RISC-V 64 (RV64IM) アセンブリにするコンパイラ。
//
// ファイルシステムにもサブプロセスにも触らない (ブラウザ上の WebAssembly からもそのまま使える)。
//
//	out, err := minicc.Compile(src)        // .lang → アセンブリ (+ アセンブリ行 → ソース行の対応)
//	exe, err := minicc.Build("fib.lang", src)  // ランタイムと一緒に ELF まで (rvasm を使う)
//	minicc.Runtime()                       // ランタイム (runtime.s) のソース
//
// エラーは *LangError (Pos に行:列) で返る。
package minicc

import (
	_ "embed"
	"regexp"
	"strconv"
	"strings"

	"rvasm/rvasm"
)

//go:embed runtime/runtime.s
var runtimeSrc string

// Runtime はランタイム (RISC-V アセンブリ) のソースを返す。
// コンパイル結果はこれと一緒にアセンブル・リンクする (エントリ _start はこちらにある)。
func Runtime() string { return runtimeSrc }

// Output はコンパイル結果。
type Output struct {
	Asm string `json:"asm"`
	// Lines[k] はアセンブリの k+1 行目を生んだソースの行番号 (1 始まり、0 は対応なし)。
	// 文の先頭に出る "# 12: ..." コメントの行から次のコメントまでがその行、
	// 関数リテラルの prologue は関数リテラルの行になる。
	Lines []int `json:"lines"`
}

// Compile はソースをアセンブリ (ユーザコード部分。runtime.s は別) に変換する。
// エラーは *LangError。
func Compile(src string) (*Output, error) {
	prog, err := Parse(src)
	if err != nil {
		return nil, err
	}
	asm, fnLines, err := generate(prog, src)
	if err != nil {
		return nil, err
	}
	return &Output{Asm: asm, Lines: lineMap(asm, fnLines)}, nil
}

var (
	reLineCmt = regexp.MustCompile(`^\t# (\d+): `)
	reLabel   = regexp.MustCompile(`^(\S+):$`)
)

func lineMap(asm string, fnLines map[string]int) []int {
	lines := strings.Split(strings.TrimSuffix(asm, "\n"), "\n")
	out := make([]int, len(lines))
	cur := 0
	for k, l := range lines {
		switch {
		case strings.HasPrefix(l, "# ---- "):
			cur = 0
			if k+1 < len(lines) { // 次の行が関数のラベル
				if m := reLabel.FindStringSubmatch(lines[k+1]); m != nil {
					cur = fnLines[m[1]]
				}
			}
		case reLineCmt.MatchString(l):
			cur, _ = strconv.Atoi(reLineCmt.FindStringSubmatch(l)[1])
		case l == "\t.data" || l == "\t.rodata" || l == "\t.text":
			cur = 0
		}
		if l != "" {
			out[k] = cur
		}
	}
	return out
}

// AsmSources はコンパイル結果 asm を rvasm.Build に渡せる形 (ユーザコード + runtime.s) にする。
// name はアセンブリの名前 (例: "prog.s")。
func AsmSources(name, asm string) []rvasm.Source {
	return []rvasm.Source{{Name: name, Text: asm}, {Name: "runtime.s", Text: runtimeSrc}}
}

// Build はソースをコンパイルし、ランタイムと一緒にアセンブル・リンクして静的 ELF 実行ファイルを返す。
// すべてメモリ上で行う。name はソースの名前 (例: "fib.lang")。
func Build(name, src string) ([]byte, error) {
	out, err := Compile(src)
	if err != nil {
		return nil, err
	}
	return rvasm.Build(AsmSources(strings.TrimSuffix(name, ".lang")+".s", out.Asm)...)
}
