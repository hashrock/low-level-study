package sim

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// 結果の表 (go test -v の最後に表示する)
var (
	mu      sync.Mutex
	results = map[string]map[string]*Result{} // プログラム名 → コア → 結果
)

func printSummary() {
	fmt.Printf("\n%-24s %10s %12s %12s %8s\n", "program", "instret", "single", "pipe", "pipe CPI")
	for _, p := range programs() {
		r := results[p.name]
		if r == nil || r["single"] == nil {
			continue
		}
		pipe, cpi := "-", "-"
		if q := r["pipe"]; q != nil {
			pipe, cpi = fmt.Sprint(q.Cycles), fmt.Sprintf("%.3f", float64(q.Cycles)/float64(q.Instret))
		}
		fmt.Printf("%-24s %10d %12d %12s %8s\n", p.name, r["single"].Instret, r["single"].Cycles, pipe, cpi)
	}
}

// TestMain で 1 回だけコンパイルした .vvp (コアの種類ごと)
var vvp = map[string]string{}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rvhw")
	if err != nil {
		panic(err)
	}
	for _, top := range []string{"single", "pipe"} {
		vvp[top] = filepath.Join(dir, top+".vvp")
		if err := Compile(top, vvp[top]); err != nil {
			println(err.Error())
			os.Exit(1)
		}
	}
	code := m.Run()
	if testing.Verbose() {
		printSummary()
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

type prog struct {
	name  string
	files []string
	argv  []string
	out   string // 期待出力のファイル (空なら emu の出力とだけ比べる)
	exit  int    // 期待する終了コード
	wait  int    // RAM の待ちサイクル
	long  bool   // -short では飛ばす (Icarus で 1 分以上かかる)
}

func programs() []prog {
	asm := func(f ...string) []string {
		for i := range f {
			f[i] = filepath.Join(Dir(), "..", "asm", "examples", f[i])
		}
		return f
	}
	tst := func(f string) []string { return []string{filepath.Join(Dir(), "tests", f)} }
	cc := func(f string) []string { return []string{filepath.Join(Dir(), "..", "cc", "examples", f+".c")} }
	ccOut := func(f string) string { return filepath.Join(Dir(), "..", "cc", "examples", f+".out") }
	return []prog{
		// 命令クラスごとの自己チェックテスト (失敗するとケース番号で exit する)
		{name: "tests/alu", files: tst("alu.s")},
		{name: "tests/word", files: tst("word.s")},
		{name: "tests/branch", files: tst("branch.s")},
		{name: "tests/loadstore", files: tst("loadstore.s")},
		{name: "tests/muldiv", files: tst("muldiv.s")},
		{name: "tests/fence", files: tst("fence.s")},
		{name: "tests/mmio", files: tst("mmio.s")},
		{name: "tests/hazard", files: tst("hazard.s")},
		{name: "tests/hazard_wait2", files: tst("hazard.s"), wait: 2},
		{name: "examples/demo", files: []string{filepath.Join(Dir(), "examples", "demo.s")}},
		{name: "tests/loadstore_wait3", files: tst("loadstore.s"), wait: 3},
		{name: "tests/mmio_wait2", files: tst("mmio.s"), wait: 2},
		// asm/examples
		{name: "asm/hello", files: asm("hello.s")},
		{name: "asm/main+lib", files: asm("main.s", "lib.s")},
		{name: "asm/fib", files: asm("fib.s")},
		// cc/examples (libc の printf は div / rem を使う)
		{name: "cc/hello", files: cc("hello"), out: ccOut("hello")},
		{name: "cc/structs", files: cc("structs"), out: ccOut("structs")},
		{name: "cc/list", files: cc("list"), out: ccOut("list")},
		{name: "cc/strings", files: cc("strings"), out: ccOut("strings")},
		{name: "cc/argv", files: cc("argv"), argv: []string{"argv", "hello", "RISC-V world", "42"}, out: ccOut("argv"), exit: 3},
		{name: "cc/structs_wait1", files: cc("structs"), out: ccOut("structs"), wait: 1},
		{name: "cc/strings_wait2", files: cc("strings"), out: ccOut("strings"), wait: 2},
		{name: "cc/sieve", files: cc("sieve"), out: ccOut("sieve")},
		{name: "cc/qsort", files: cc("qsort"), out: ccOut("qsort"), long: true},
		{name: "cc/fib", files: cc("fib"), out: ccOut("fib"), long: true},
	}
}

// TestLockstep は全プログラムを Verilog と emu で 1 命令ずつ突き合わせて実行する。
// 単一サイクル版 (single/...) と 5 段パイプライン版 (pipe/...) の両方で。
func TestLockstep(t *testing.T) {
	for _, top := range []string{"single", "pipe"} {
		for _, p := range programs() {
			testProgram(t, top, p)
		}
	}
}

func testProgram(t *testing.T, top string, p prog) {
	t.Run(top+"/"+p.name, func(t *testing.T) {
		if p.long && testing.Short() {
			t.Skip("long (-short)")
		}
		t.Parallel()
		r, err := Lockstep(Options{VVP: vvp[top], Files: p.files, Argv: p.argv, Wait: p.wait, WorkDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if r.ExitCode != p.exit {
			t.Fatalf("exit code %d (自己チェックテストならケース番号)\n%s", r.ExitCode, r.Stdout)
		}
		if p.out != "" {
			want, err := os.ReadFile(p.out)
			if err != nil {
				t.Fatal(err)
			}
			if r.Stdout != string(want) {
				t.Fatalf("output differs from %s:\n--- got\n%s\n--- want\n%s", p.out, r.Stdout, want)
			}
		}
		t.Logf("%s/%s: cycles=%d instret=%d CPI=%.3f", top, p.name, r.Cycles, r.Instret, float64(r.Cycles)/float64(r.Instret))
		mu.Lock()
		if results[p.name] == nil {
			results[p.name] = map[string]*Result{}
		}
		results[p.name][top] = r
		mu.Unlock()
	})
}
