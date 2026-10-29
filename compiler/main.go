// minicc: minilang → RISC-V 64 (RV64IM) ネイティブコンパイラ。
//
//	go run . emit  prog.lang              アセンブリを標準出力へ
//	go run . build prog.lang [-o prog]    .s を出力しランタイムとリンク (rvasm をライブラリとして使用)
//	go run . run   prog.lang              build して ../emu で実行
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"minicc/minicc"
	"rvasm/rvasm"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: minicc emit|build|run prog.lang [-o out]")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	cmd, file := os.Args[1], os.Args[2]
	out := strings.TrimSuffix(filepath.Base(file), ".lang")
	if len(os.Args) == 5 && os.Args[3] == "-o" {
		out = os.Args[4]
	} else if len(os.Args) != 3 {
		usage()
	}
	src, err := os.ReadFile(file)
	check(err)
	res, err := minicc.Compile(string(src))
	check(err)
	asm := res.Asm
	switch cmd {
	case "emit":
		fmt.Print(asm)
	case "build":
		check(Build(asm, out))
	case "run":
		tmp, err := os.MkdirTemp("", "minicc")
		check(err)
		defer os.RemoveAll(tmp)
		prog := filepath.Join(tmp, "prog")
		check(Build(asm, prog))
		c := goRun("emu")("run", prog)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.RemoveAll(tmp)
				os.Exit(ee.ExitCode())
			}
			check(err)
		}
	default:
		usage()
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// srcDir は compiler/ ディレクトリ。カレントディレクトリを仮定する。
func srcDir() string {
	if d := os.Getenv("MINICC_DIR"); d != "" {
		return d
	}
	d, _ := os.Getwd()
	return d
}

// toolDir は隣の ../emu や ../lang。
func toolDir(name string) string { return filepath.Join(srcDir(), "..", name) }

// goRun は隣のプロジェクトを `go run .` で起動するコマンドを作る。
func goRun(name string) func(args ...string) *exec.Cmd {
	return func(args ...string) *exec.Cmd {
		c := exec.Command("go", append([]string{"run", "."}, args...)...)
		c.Dir = toolDir(name)
		return c
	}
}

// Build は out.s を書き出し、runtime.s と一緒にアセンブル+リンクする (rvasm をライブラリとして使う)。
func Build(asm, out string) error {
	if err := os.WriteFile(out+".s", []byte(asm), 0o644); err != nil {
		return err
	}
	exe, err := rvasm.Build(minicc.AsmSources(filepath.Base(out)+".s", asm)...)
	if err != nil {
		return err
	}
	return os.WriteFile(out, exe, 0o755)
}
