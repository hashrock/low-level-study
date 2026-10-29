// rvcc: C のサブセットを RISC-V 64 (RV64IM) アセンブリにするコンパイラ。
//
//	go run . emit  [-O0|-O1|-O2] prog.c                 アセンブリを標準出力へ
//	go run . build [-O0|-O1|-O2] a.c b.c ... [-o prog]   libc とリンクして ELF を作る (rvasm をライブラリとして使う)
//	go run . run   [-O0|-O1|-O2] [--stats] prog.c [args...]   build して ../emu で実行
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"rvcc/rvcc"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  rvcc emit  [-O0|-O1|-O2] prog.c
  rvcc build [-O0|-O1|-O2] a.c [b.c ...] [-o prog]
  rvcc run   [-O0|-O1|-O2] [--stats] prog.c [more.c ...] [args...]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	cmd := os.Args[1]
	opt, stats, out := 2, false, "a.out"
	var files, progArgs []string
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case len(progArgs) > 0 || len(files) > 0 && !strings.HasSuffix(a, ".c") && cmd == "run":
			progArgs = append(progArgs, a)
		case a == "-O0" || a == "-O1" || a == "-O2":
			opt = int(a[2] - '0')
		case a == "--stats":
			stats = true
		case a == "-o" && i+1 < len(args):
			out = args[i+1]
			i++
		case strings.HasSuffix(a, ".c"):
			files = append(files, a)
		default:
			usage()
		}
	}
	if len(files) == 0 {
		usage()
	}
	switch cmd {
	case "emit":
		srcs, err := readSources(files)
		check(err)
		for _, s := range srcs {
			o, err := rvcc.Compile(s.Name, s.Text, opt)
			check(err)
			fmt.Print(o.Asm)
		}
	case "build":
		check(Build(files, out, opt))
	case "run":
		tmp, err := os.MkdirTemp("", "rvcc")
		check(err)
		prog := filepath.Join(tmp, "prog")
		err = Build(files, prog, opt)
		if err != nil {
			os.RemoveAll(tmp)
			check(err)
		}
		emuArgs := []string{"run"}
		if stats {
			emuArgs = append(emuArgs, "--stats")
		}
		c := goRun("emu")(append(append(emuArgs, prog), progArgs...)...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = c.Run()
		os.RemoveAll(tmp)
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		check(err)
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

// readSources は .c ファイルを読み込む (名前はベース名)。
func readSources(files []string) ([]rvcc.Source, error) {
	var srcs []rvcc.Source
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, rvcc.Source{Name: filepath.Base(f), Text: string(b)})
	}
	return srcs, nil
}

// Build は各 .c と libc.c をコンパイルし、crt0.s と一緒にアセンブル・リンクして out に書く。
// コンパイルからリンクまで (rvcc / rvasm パッケージ) はすべてメモリ上で行う。
func Build(files []string, out string, opt int) error {
	srcs, err := readSources(files)
	if err != nil {
		return err
	}
	exe, err := rvcc.Build(srcs, opt)
	if err != nil {
		return err
	}
	return os.WriteFile(out, exe, 0o755)
}

// srcDir はこのソースのあるディレクトリ (cc/)。go run / go test のどちらでも正しく求まる。
func srcDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

func toolDir(name string) string { return filepath.Join(srcDir(), "..", name) }

// goRun は隣のプロジェクト (../emu) を `go run .` で起動するコマンドを作る。
func goRun(name string) func(args ...string) *exec.Cmd {
	return func(args ...string) *exec.Cmd {
		c := exec.Command("go", append([]string{"run", "."}, args...)...)
		c.Dir = toolDir(name)
		return c
	}
}
