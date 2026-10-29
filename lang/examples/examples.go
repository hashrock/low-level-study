// Package examples は examples/*.lang とその期待出力 *.out を埋め込む
// (ブラウザ版のサンプル選択とテストで使う)。
package examples

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed *.lang *.out
var files embed.FS

type Example struct {
	Name string `json:"name"` // 拡張子なしのファイル名
	Src  string `json:"src"`
	Out  string `json:"out"` // 期待される出力 (エラーメッセージを含む)
}

// All はすべてのサンプルをファイル名順に返す。
func All() []Example {
	names, _ := fs.Glob(files, "*.lang")
	var out []Example
	for _, n := range names {
		src, _ := files.ReadFile(n)
		want, _ := files.ReadFile(strings.TrimSuffix(n, ".lang") + ".out")
		out = append(out, Example{Name: strings.TrimSuffix(n, ".lang"), Src: string(src), Out: string(want)})
	}
	return out
}
