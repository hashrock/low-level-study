package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../../.."

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func compileOK(t *testing.T, s *session, lang, name, src string, opt int) compileResult {
	t.Helper()
	req, _ := json.Marshal(compileReq{Lang: lang, Name: name, Src: src, Opt: opt})
	var res compileResult
	if err := json.Unmarshal([]byte(s.compile(string(req))), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("%s: compile failed: %+v", name, res.Error)
	}
	return res
}

// runAll は最後まで実行して stdout+stderr と終了コードを返す。
func runAll(t *testing.T, s *session, args string) (string, runResult) {
	t.Helper()
	if err := s.start(args, "", 0); err != "" {
		t.Fatal(err)
	}
	var out strings.Builder
	var r runResult
	for i := 0; i < 10000; i++ {
		if err := json.Unmarshal([]byte(s.run(1_000_000)), &r); err != nil {
			t.Fatal(err)
		}
		for _, c := range r.Out {
			out.WriteString(c.Text)
		}
		if r.Exited || r.Budget {
			break
		}
	}
	return out.String(), r
}

func exampleArgs(src string) string {
	first, _, _ := strings.Cut(src, "\n")
	spec, _ := strings.CutPrefix(first, "// args: ")
	if spec == first {
		return ""
	}
	return spec
}

func TestCExamples(t *testing.T) {
	files, _ := filepath.Glob(root + "/cc/examples/*.c")
	if len(files) == 0 {
		t.Fatal("no examples")
	}
	for _, f := range files {
		for _, opt := range []int{0, 2} {
			s := newSession()
			src := mustRead(t, f)
			compileOK(t, s, "c", filepath.Base(f), src, opt)
			got, _ := runAll(t, s, exampleArgs(src))
			want := mustRead(t, strings.TrimSuffix(f, ".c")+".out")
			if got != want {
				t.Errorf("%s -O%d:\n--- got ---\n%s--- want ---\n%s", f, opt, got, want)
			}
		}
	}
}

func TestLangExamples(t *testing.T) {
	files, _ := filepath.Glob(root + "/compiler/examples/*.lang")
	if len(files) == 0 {
		t.Fatal("no examples")
	}
	for _, f := range files {
		s := newSession()
		compileOK(t, s, "lang", filepath.Base(f), mustRead(t, f), 0)
		got, _ := runAll(t, s, "")
		want := mustRead(t, strings.TrimSuffix(f, ".lang")+".out")
		want = strings.ReplaceAll(want, "exit status 1\n", "")
		if got != want {
			t.Errorf("%s:\n--- got ---\n%s--- want ---\n%s", f, got, want)
		}
	}
}

func TestMapping(t *testing.T) {
	s := newSession()
	src := mustRead(t, root+"/cc/examples/fib.c")
	res := compileOK(t, s, "c", "fib.c", src, 0)
	user := -1
	for k, f := range res.Files {
		if f.User {
			user = k
		}
	}
	if user < 0 || res.Files[user].Name != "fib.s" {
		t.Fatalf("user file: %+v", res.Files)
	}
	// fib.c の各行が、命令の listing から引けること (少なくとも 1 つ)
	srcLines := map[int]int{}
	for _, l := range res.Listing {
		if l.File == user && l.Kind == "inst" && l.SrcLine > 0 {
			srcLines[l.SrcLine]++
		}
	}
	if len(srcLines) < 5 {
		t.Errorf("too few source lines mapped: %v", srcLines)
	}
	// la は auipc + addi に展開され、PCREL のリロケーションが付く
	var hi, lo bool
	for _, r := range res.Relocs {
		if r.Type == "R_RISCV_PCREL_HI20" {
			hi = true
			if r.Index < 0 || res.Listing[r.Index].Mnemonic != "auipc" {
				t.Errorf("PCREL_HI20 not on auipc: %+v", r)
			}
		}
		if r.Type == "R_RISCV_PCREL_LO12_I" {
			lo = true
		}
	}
	if !hi || !lo {
		t.Errorf("missing PCREL relocs")
	}
	if res.ELF == nil || len(res.ELF.Segments) == 0 || res.ELF.Entry != res.Entry {
		t.Errorf("elf: %+v", res.ELF)
	}
	names := map[string]bool{}
	for _, sct := range res.ELF.Sections {
		names[sct.Name] = true
	}
	for _, n := range []string{".text", ".rodata", ".symtab"} {
		if !names[n] {
			t.Errorf("section %s missing: %v", n, names)
		}
	}
}

func TestStepLine(t *testing.T) {
	s := newSession()
	compileOK(t, s, "c", "fib.c", mustRead(t, root+"/cc/examples/fib.c"), 0)
	if err := s.start("", "", 0); err != "" {
		t.Fatal(err)
	}
	var st stateJSON
	json.Unmarshal([]byte(s.state()), &st)
	if st.Idx < 0 || s.res.Listing[st.Idx].Sym != "_start" {
		t.Fatalf("not at _start: %+v", st)
	}
	seen := map[int]bool{}
	for i := 0; i < 20; i++ {
		s.stepLine(10_000_000)
		l := s.srcLine(s.m.CPU.PC)
		if l == 0 && !s.m.CPU.Halted {
			t.Fatalf("stepLine stopped outside user code at %x", s.m.CPU.PC)
		}
		seen[l] = true
	}
	if len(seen) < 3 {
		t.Errorf("stepLine visited too few lines: %v", seen)
	}
	var sys []syscallJSON
	json.Unmarshal([]byte(s.syscalls(0)), &sys)
	if len(sys) == 0 || sys[0].Name != "write" || !strings.Contains(sys[0].Args, "fd=1") {
		t.Errorf("syscalls: %+v", sys)
	}
}

func TestErrors(t *testing.T) {
	s := newSession()
	var res compileResult
	json.Unmarshal([]byte(s.compile(`{"lang":"c","name":"a.c","src":"int main() {\n  return x;\n}\n","opt":2}`)), &res)
	if res.OK || res.Error == nil || !res.Error.User || res.Error.Line != 2 {
		t.Errorf("C error: %+v", res.Error)
	}
	json.Unmarshal([]byte(s.compile(`{"lang":"lang","name":"a.lang","src":"let x = \nprint(x +)\n"}`)), &res)
	if res.OK || res.Error == nil || res.Error.Line == 0 {
		t.Errorf("lang error: %+v", res.Error)
	}
	// 無限ループは budget で止まる
	compileOK(t, s, "c", "loop.c", "int main() { for (;;) {} }\n", 2)
	s.start("", "", 1000)
	_, r := runAll(t, s, "")
	_ = r
	s.start("", "", 5000)
	r = runResult{}
	json.Unmarshal([]byte(s.run(1e6)), &r)
	if !r.Budget || r.Instret != 5000 {
		t.Errorf("budget: %+v", r)
	}
}

func TestBench(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	cases := []struct {
		lang, file string
		opt        int
		static     int
		minInst    uint64
		maxInst    uint64
	}{
		{"c", "fib25.c", 0, 0, 8_000_000, 10_000_000},
		{"c", "fib25.c", 2, 26, 4_800_000, 4_950_000},
		{"lang", "fib25.lang", 0, 99, 15_000_000, 18_000_000},
	}
	for _, c := range cases {
		s := newSession()
		var info benchInfo
		json.Unmarshal([]byte(s.benchStart(c.lang, c.file, mustRead(t, root+"/cc/bench/"+c.file), c.opt)), &info)
		if info.Error != "" {
			t.Fatal(info.Error)
		}
		if c.static != 0 && info.Static != c.static {
			t.Errorf("%s -O%d: static %d, want %d", c.file, c.opt, info.Static, c.static)
		}
		var r benchResult
		for !r.Done {
			json.Unmarshal([]byte(s.benchRun(5_000_000)), &r)
		}
		t.Logf("%s -O%d: static %d words / %d asm lines (%s), dynamic %d", c.file, c.opt, info.Static, info.Lines, info.Func, r.Instret)
		if r.Out != "75025\n" || r.Instret < c.minInst || r.Instret > c.maxInst {
			t.Errorf("%s -O%d: out %q instret %d", c.file, c.opt, r.Out, r.Instret)
		}
	}
}
