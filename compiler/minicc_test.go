package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"minicc/minicc"
)

var update = flag.Bool("update", false, "examples/*.out を現在の出力で書き換える")

// ../emu を一度だけ go build しておく (go run を毎回するより速い)。
// コンパイル・アセンブル・リンクは minicc / rvasm パッケージでプロセス内で行う。
var emuBin, binErr string

func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "minicc-test")
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

// compileAndRun はソースをコンパイル → アセンブル → エミュレータで実行し、出力 (stdout+stderr) と終了コードを返す。
func compileAndRun(t *testing.T, src string) (string, int) {
	t.Helper()
	if binErr != "" {
		t.Skip(binErr)
	}
	exe, err := minicc.Build("prog.lang", src)
	if err != nil {
		if _, ok := err.(*minicc.LangError); ok {
			return err.Error() + "\n", -1
		}
		t.Fatal(err)
	}
	prog := filepath.Join(t.TempDir(), "prog")
	if err := os.WriteFile(prog, exe, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(emuBin, "run", "--max-insts", "2000000000", prog).CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestExamples(t *testing.T) {
	files, _ := filepath.Glob("examples/*.lang")
	if len(files) == 0 {
		t.Fatal("no examples")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, _ := os.ReadFile(f)
			got, _ := compileAndRun(t, string(src))
			outFile := strings.TrimSuffix(f, ".lang") + ".out"
			if *update {
				os.WriteFile(outFile, []byte(got), 0o644)
				return
			}
			want, err := os.ReadFile(outFile)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("output mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
			}
		})
	}
}

// ../lang のインタプリタと同じ出力になるか (「minicc 専用」の例は除く)。
func TestAgainstInterpreter(t *testing.T) {
	langDir := toolDir("lang")
	if _, err := os.Stat(filepath.Join(langDir, "vm.go")); err != nil {
		t.Skip("../lang not found")
	}
	files, _ := filepath.Glob("examples/*.lang")
	for _, f := range files {
		src, _ := os.ReadFile(f)
		if strings.Contains(string(src), "minicc 専用") {
			continue
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			got, _ := compileAndRun(t, string(src))
			abs, _ := filepath.Abs(f)
			c := exec.Command("go", "run", ".", abs)
			c.Dir = langDir
			out, _ := c.CombinedOutput()
			want := strings.ReplaceAll(string(out), abs+":", "") // エラーのファイル名接頭辞を除く
			want = strings.ReplaceAll(want, "exit status 1\n", "")
			if got != want {
				t.Errorf("differs from interpreter\n--- minicc ---\n%s--- lang ---\n%s", got, want)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{`print(1 + "a")`, "1:9: runtime error: cannot apply + to number and string"},
		{`let x = 1 - nil`, "1:11: runtime error: cannot apply - to number and nil"},
		{`print("a" < 1)`, "cannot apply < to string and number"},
		{`print(1 / 0)`, "1:9: runtime error: division by zero"},
		{`print([1, 2][5])`, "index 5 out of range (length 2)"},
		{`print([1]["a"])`, "index must be a number, got string"},
		{`const m = {a: 1}
print(m.b)`, `2:8: runtime error: key "b" not found in map (use has() to check)`},
		{`fn f(a, b) { return a }
f(1)`, "2:2: runtime error: f expects 2 argument(s) but got 1"},
		{`let n = 3
n()`, "cannot call number"},
		{`for (x in 5) {}`, "cannot iterate over number"},
		{`print(-"s")`, "cannot apply - to string"},
		{`push(1, 2)`, "push: first argument must be an array, got number"},
		{"fn f(n) { return f(n + 1) }\nf(0)", "runtime error: stack overflow (too deep recursion)"},
		{`print(y)`, "1:7: compile error: undefined variable y"},
		{`const c = 1
c = 2`, "cannot assign to const c"},
		{`print(1.5)`, "only integers are supported"},
		{`{ let x = 1 } print(x)`, "undefined variable x"},
	}
	for _, c := range cases {
		got, code := compileAndRun(t, c.src)
		if !strings.Contains(got, c.want) || code == 0 {
			t.Errorf("%q: got %q (exit %d), want %q", c.src, got, code, c.want)
		}
	}
}

func TestEmitIsDeterministic(t *testing.T) {
	src, _ := os.ReadFile("examples/closures.lang")
	a, err := minicc.Compile(string(src))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := minicc.Compile(string(src))
	if a.Asm != b.Asm {
		t.Error("emit output differs between runs")
	}
}
