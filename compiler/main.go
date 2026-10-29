// minicc: minilang → RISC-V 64 (RV64IM) ネイティブコンパイラ。
//
//	go run . emit  prog.lang              アセンブリを標準出力へ
//	go run . build prog.lang [-o prog]    .s を出力し ../asm でランタイムとリンク
//	go run . run   prog.lang              build して ../emu で実行
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	asm, err := Compile(string(src))
	check(err)
	switch cmd {
	case "emit":
		fmt.Print(asm)
	case "build":
		check(Build(asm, out, goRun("asm")))
	case "run":
		tmp, err := os.MkdirTemp("", "minicc")
		check(err)
		defer os.RemoveAll(tmp)
		prog := filepath.Join(tmp, "prog")
		check(Build(asm, prog, goRun("asm")))
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

// Compile はソースをアセンブリ (ユーザコード部分。runtime.s は別) に変換する。
func Compile(src string) (string, error) {
	prog, err := Parse(src)
	if err != nil {
		return "", err
	}
	return Generate(prog, src)
}

// srcDir は compiler/ ディレクトリ (runtime/runtime.s がある場所)。カレントディレクトリを仮定する。
func srcDir() string {
	if d := os.Getenv("MINICC_DIR"); d != "" {
		return d
	}
	d, _ := os.Getwd()
	return d
}

// toolDir は隣の ../asm や ../emu。
func toolDir(name string) string { return filepath.Join(srcDir(), "..", name) }

// goRun は隣のプロジェクトを `go run .` で起動するコマンドを作る。
func goRun(name string) func(args ...string) *exec.Cmd {
	return func(args ...string) *exec.Cmd {
		c := exec.Command("go", append([]string{"run", "."}, args...)...)
		c.Dir = toolDir(name)
		return c
	}
}

// Build は out.s を書き出し、runtime.s と一緒にアセンブラ (../asm) でアセンブル+リンクする。
func Build(asm, out string, assembler func(args ...string) *exec.Cmd) error {
	out, _ = filepath.Abs(out)
	sfile := out + ".s"
	if err := os.WriteFile(sfile, []byte(asm), 0o644); err != nil {
		return err
	}
	rt := filepath.Join(srcDir(), "runtime", "runtime.s")
	if msg, err := assembler("build", sfile, rt, "-o", out).CombinedOutput(); err != nil {
		return fmt.Errorf("asm failed: %v\n%s", err, msg)
	}
	return nil
}
