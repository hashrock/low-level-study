// rvcc: C のサブセットを RISC-V 64 (RV64IM) アセンブリにするコンパイラ。
//
//	go run . emit  [-O0|-O1|-O2] prog.c                 アセンブリを標準出力へ
//	go run . build [-O0|-O1|-O2] a.c b.c ... [-o prog]   ../asm で libc とリンクして ELF を作る
//	go run . run   [-O0|-O1|-O2] [--stats] prog.c [args...]   build して ../emu で実行
package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed libc/libc.h libc/libc.c libc/crt0.s
var libcFS embed.FS

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
		for _, f := range files {
			asm, err := CompileFile(f, opt)
			check(err)
			fmt.Print(asm)
		}
	case "build":
		check(Build(files, out, opt, goRun("asm")))
	case "run":
		tmp, err := os.MkdirTemp("", "rvcc")
		check(err)
		prog := filepath.Join(tmp, "prog")
		err = Build(files, prog, opt, goRun("asm"))
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

// Compile は 1 つの .c ファイル (翻訳単位) をアセンブリにする。
// 先頭に libc.h (プロトタイプ宣言) を暗黙に読み込む: プリプロセッサが無いので #include は無視される。
func Compile(name, src string, opt int) (string, error) {
	hdr, _ := libcFS.ReadFile("libc/libc.h")
	pre, err := Tokenize("libc.h", string(hdr))
	if err != nil {
		return "", err
	}
	toks, err := Tokenize(name, src)
	if err != nil {
		return "", err
	}
	prog, err := Parse(append(pre[:len(pre)-1], toks...))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# %s (rvcc -O%d)\n", name, opt) + Generate(prog, opt), nil
}

func CompileFile(path string, opt int) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Compile(filepath.Base(path), string(src), opt)
}

// Build は各 .c と libc.c をコンパイルし、crt0.s と一緒に ../asm でリンクする。
func Build(files []string, out string, opt int, assembler func(args ...string) *exec.Cmd) error {
	out, _ = filepath.Abs(out)
	dir, err := os.MkdirTemp("", "rvcc-build")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var sfiles []string
	write := func(name, asm string) error {
		path := filepath.Join(dir, fmt.Sprintf("%d_%s", len(sfiles), name))
		sfiles = append(sfiles, path)
		return os.WriteFile(path, []byte(asm), 0o644)
	}
	crt0, _ := libcFS.ReadFile("libc/crt0.s")
	libc, _ := libcFS.ReadFile("libc/libc.c")
	if err := write("crt0.s", string(crt0)); err != nil {
		return err
	}
	asm, err := Compile("libc.c", string(libc), opt)
	if err != nil {
		return fmt.Errorf("libc: %v", err)
	}
	if err := write("libc.s", asm); err != nil {
		return err
	}
	for _, f := range files {
		asm, err := CompileFile(f, opt)
		if err != nil {
			return err
		}
		if err := write(strings.TrimSuffix(filepath.Base(f), ".c")+".s", asm); err != nil {
			return err
		}
	}
	if msg, err := assembler(append(append([]string{"build"}, sfiles...), "-o", out)...).CombinedOutput(); err != nil {
		return fmt.Errorf("asm failed: %v\n%s", err, msg)
	}
	return nil
}

// srcDir はこのソースのあるディレクトリ (cc/)。go run / go test のどちらでも正しく求まる。
func srcDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

func toolDir(name string) string { return filepath.Join(srcDir(), "..", name) }

// goRun は隣のプロジェクト (../asm, ../emu) を `go run .` で起動するコマンドを作る。
func goRun(name string) func(args ...string) *exec.Cmd {
	return func(args ...string) *exec.Cmd {
		c := exec.Command("go", append([]string{"run", "."}, args...)...)
		c.Dir = toolDir(name)
		return c
	}
}
