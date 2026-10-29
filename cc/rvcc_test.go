package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"rvcc/rvcc"
)

var update = flag.Bool("update", false, "examples/*.out を rvcc の出力で書き換える")

// ../emu を一度だけ go build しておく (毎回 go run するより速い)。
// コンパイル・アセンブル・リンクは rvcc / rvasm パッケージでプロセス内で行う。
var emuBin, binErr string

func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "rvcc-test")
	if err != nil {
		panic(err)
	}
	emuBin = filepath.Join(dir, "rvemu")
	c := exec.Command("go", "build", "-o", emuBin, ".")
	c.Dir = toolDir("emu")
	if out, err := c.CombinedOutput(); err != nil {
		binErr = fmt.Sprintf("cannot build ../emu: %v\n%s", err, out)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	out   string
	code  int
	insts int64 // emu --stats の実行命令数
}

// run は files をコンパイル → アセンブル → エミュレータで実行する。
func run(t *testing.T, files []string, opt int, args ...string) result {
	t.Helper()
	if binErr != "" {
		t.Skip(binErr)
	}
	prog := filepath.Join(t.TempDir(), "prog")
	if err := Build(files, prog, opt); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(emuBin, append([]string{"run", "--stats", "--max-insts", "500000000", prog}, args...)...)
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	r := result{out: stdout.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(\d+) instructions`).FindStringSubmatch(stderr.String()); m != nil {
		r.insts, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if strings.Contains(stderr.String(), "stopped") {
		t.Errorf("emulator stopped:\n%s", stderr.String())
	}
	return r
}

// exampleArgs は先頭行の "// args: a "b c"" を引数にする。
func exampleArgs(src string) []string {
	first, _, _ := strings.Cut(src, "\n")
	spec, ok := strings.CutPrefix(first, "// args: ")
	if !ok {
		return nil
	}
	var args []string
	for i, part := range strings.Split(spec, `"`) {
		if i%2 == 1 {
			args = append(args, part)
		} else {
			args = append(args, strings.Fields(part)...)
		}
	}
	return args
}

func examples(t *testing.T) []string {
	files, _ := filepath.Glob("examples/*.c")
	if len(files) == 0 {
		t.Fatal("no examples")
	}
	return files
}

func TestExamples(t *testing.T) {
	for _, f := range examples(t) {
		src, _ := os.ReadFile(f)
		outFile := strings.TrimSuffix(f, ".c") + ".out"
		for opt := 0; opt <= 2; opt++ {
			t.Run(fmt.Sprintf("%s/O%d", filepath.Base(f), opt), func(t *testing.T) {
				got := run(t, []string{f}, opt, exampleArgs(string(src))...)
				if *update && opt == 2 {
					os.WriteFile(outFile, []byte(got.out), 0o644)
					return
				}
				want, err := os.ReadFile(outFile)
				if err != nil {
					t.Fatal(err)
				}
				if got.out != string(want) {
					t.Errorf("output mismatch\n--- got ---\n%s--- want ---\n%s", got.out, want)
				}
			})
		}
	}
}

// tests/test.c: 機能ごとの assert_eq。失敗すると行番号を出して exit(1)。
func TestSuite(t *testing.T) {
	for opt := 0; opt <= 2; opt++ {
		t.Run(fmt.Sprintf("O%d", opt), func(t *testing.T) {
			r := run(t, []string{"tests/test.c"}, opt)
			if r.out != "OK\n" || r.code != 0 {
				t.Errorf("exit %d\n%s", r.code, r.out)
			}
		})
	}
}

// ホストの C コンパイラ (macOS なら Apple clang) で同じソースを動かし、期待出力と一致するか。
// 期待出力 (*.out) そのものの正しさを確かめる「答え合わせ」。
func TestHostCC(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no host cc")
	}
	dir := t.TempDir()
	hostRun := func(t *testing.T, args []string, srcs ...string) string {
		bin := filepath.Join(dir, "host")
		if out, err := exec.Command(cc, append([]string{"-w", "-o", bin}, srcs...)...).CombinedOutput(); err != nil {
			t.Fatalf("host cc: %v\n%s", err, out)
		}
		out, _ := exec.Command(bin, args...).Output()
		return string(out)
	}
	for _, f := range examples(t) {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, _ := os.ReadFile(f)
			want, _ := os.ReadFile(strings.TrimSuffix(f, ".c") + ".out")
			if got := hostRun(t, exampleArgs(string(src)), f); got != string(want) {
				t.Errorf("host cc output differs\n--- host ---\n%s--- want ---\n%s", got, want)
			}
		})
	}
	t.Run("test.c", func(t *testing.T) {
		if got := hostRun(t, nil, "tests/test.c", "tests/host_shim.c"); got != "OK\n" {
			t.Errorf("host cc: %s", got)
		}
	})
}

// 2 つの翻訳単位を別々にコンパイルしてリンクする (extern 変数・関数の参照)。
func TestMultiFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.c")
	b := filepath.Join(dir, "b.c")
	os.WriteFile(a, []byte("extern int counter;\nint bump(int n);\nint main() { bump(2); bump(3); printf(\"%d\\n\", counter); return 0; }\n"), 0o644)
	os.WriteFile(b, []byte("int counter = 10;\nstatic int twice(int n) { return n * 2; }\nint bump(int n) { counter += twice(n); return counter; }\n"), 0o644)
	if r := run(t, []string{a, b}, 2); r.out != "20\n" {
		t.Errorf("got %q", r.out)
	}
}

// 最適化の効果: 実行命令数を表示する (go test -v -run TestBench)。
func TestBench(t *testing.T) {
	for _, f := range []string{"bench/fib25.c", "examples/sieve.c", "examples/qsort.c"} {
		var counts [3]int64
		var outs [3]string
		for opt := 0; opt <= 2; opt++ {
			r := run(t, []string{f}, opt)
			counts[opt], outs[opt] = r.insts, r.out
		}
		t.Logf("%-18s -O0 %9d  -O1 %9d  -O2 %9d instructions", f, counts[0], counts[1], counts[2])
		if outs[0] != outs[2] || !(counts[2] < counts[1] && counts[1] < counts[0]) {
			t.Errorf("%s: optimization changed output or did not help", f)
		}
	}
}

func TestEmitIsDeterministic(t *testing.T) {
	src, err := os.ReadFile("tests/test.c")
	if err != nil {
		t.Fatal(err)
	}
	a, err := rvcc.Compile("test.c", string(src), 2)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := rvcc.Compile("test.c", string(src), 2); a.Asm != b.Asm {
		t.Error("emit output differs between runs")
	}
}
