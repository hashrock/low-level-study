package minilang

import (
	"encoding/json"
	"strings"
	"testing"

	"minilang/examples"
)

// runSession は mode で最後まで進め、CLI と同じ形式 (出力 + エラー) の文字列を返す。
func runSession(t *testing.T, src, mode string, budget int, check func(*Session)) string {
	t.Helper()
	s, err := Compile(src)
	if err != nil {
		return err.Error() + "\n"
	}
	for i := 0; ; i++ {
		if i > 10_000_000 {
			t.Fatal("did not finish")
		}
		r := s.Step(mode, budget)
		if check != nil {
			check(s)
		}
		if r.Status == "error" {
			return s.AllOutput() + r.Error.Text + "\n"
		}
		if r.Status == "done" {
			return s.AllOutput()
		}
	}
}

// 1 命令ずつ・1 行ずつ・小さな予算で区切って実行しても、一気に実行したのと同じ出力になる。
func TestSteppingMatchesExamples(t *testing.T) {
	for _, ex := range examples.All() {
		for _, mode := range []struct {
			name   string
			budget int
		}{{"instr", 1}, {"line", 1000}, {"out", 1000}, {"run", 7}} {
			t.Run(ex.Name+"/"+mode.name, func(t *testing.T) {
				got := runSession(t, ex.Src, mode.name, mode.budget, nil)
				if got != ex.Out {
					t.Errorf("got\n%s\nwant\n%s", got, ex.Out)
				}
			})
		}
	}
}

// すべての命令の後でスナップショットが作れて JSON にでき、スタックの数が sp と合う。
func TestSnapshotEveryStep(t *testing.T) {
	for _, ex := range examples.All() {
		t.Run(ex.Name, func(t *testing.T) {
			runSession(t, ex.Src, "instr", 1, func(s *Session) {
				if n := s.VM.Steps(); n > 3000 && n%997 != 0 {
					return // 長いサンプルは間引く
				}
				snap := s.VM.Snapshot()
				if _, err := json.Marshal(snap); err != nil {
					t.Fatal(err)
				}
				if len(snap.Stack)+snap.StackOmitted != snap.SP {
					t.Fatalf("stack %d+%d != sp %d", len(snap.Stack), snap.StackOmitted, snap.SP)
				}
				if len(snap.Frames) == 0 || snap.Cur.Op == "?" {
					t.Fatalf("bad snapshot %+v", snap.Cur)
				}
			})
		})
	}
}

func TestBudgetStopsInfiniteLoop(t *testing.T) {
	s, err := Compile("let i = 0\nwhile (true) { i += 1 }")
	if err != nil {
		t.Fatal(err)
	}
	r := s.Step("run", 1000)
	if r.Status != "budget" || r.Steps != 1000 {
		t.Fatalf("got %+v", r)
	}
	// 1 行ずつ進めると、同じ行のループでも 1 周ごとに止まる
	r = s.Step("line", 1000)
	if r.Status != "paused" {
		t.Fatalf("line step in loop: %+v", r)
	}
}

func TestStepOverDoesNotEnterCalls(t *testing.T) {
	src := "fn f(n) {\n  return n + 1\n}\nlet a = f(1)\nlet b = f(a)\nprint(b)"
	s, _ := Compile(src)
	lines := []int{}
	for {
		snap := s.VM.Snapshot()
		if len(snap.Frames) != 1 {
			t.Fatalf("stepped into a call at line %d", snap.Cur.Line)
		}
		lines = append(lines, snap.Cur.Line)
		if r := s.Step("line", 1000); r.Status != "paused" {
			break
		}
	}
	if got := strings.Trim(strings.Join(strings.Fields(strings.Trim(strings.Replace(strings.Trim(jsonStr(lines), "[]"), ",", " ", -1), " ")), " "), " "); got != "1 4 5 6" {
		t.Errorf("lines visited: %s", got)
	}
	if s.AllOutput() != "3\n" {
		t.Errorf("output %q", s.AllOutput())
	}
}

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestStepOut(t *testing.T) {
	s, _ := Compile("fn f() {\n  let x = 1\n  return x\n}\nprint(f())")
	for s.VM.Depth() < 2 {
		s.Step("instr", 1)
	}
	if r := s.Step("out", 1000); r.Status != "paused" || s.VM.Depth() != 1 {
		t.Fatalf("%+v depth %d", r, s.VM.Depth())
	}
}

func runToEnd(t *testing.T, src string) *Snapshot {
	t.Helper()
	s, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Step("run", 1_000_000); r.Status != "done" {
		t.Fatalf("%+v", r)
	}
	return s.VM.Snapshot()
}

func TestSnapshotClosureCells(t *testing.T) {
	// 反復ごとの束縛: 3 つのクロージャは別々のセルを捕捉する
	snap := runToEnd(t, "let fs = []\nfor (i in range(3)) { push(fs, fn() => i * 100) }")
	if len(snap.Closures) != 3 || len(snap.Cells) != 3 {
		t.Fatalf("closures %d cells %d: %s", len(snap.Closures), len(snap.Cells), jsonStr(snap.Cells))
	}
	vals := map[string]bool{}
	for _, c := range snap.Cells {
		vals[c.Value.Text] = true
		if len(c.Refs) < 1 || !strings.Contains(c.Refs[0], "<anon>") {
			t.Errorf("cell %+v", c)
		}
	}
	if !vals["0"] || !vals["1"] || !vals["2"] {
		t.Errorf("cells %s", jsonStr(snap.Cells))
	}
	// 2 つのクロージャが 1 つのセルを共有する
	snap = runToEnd(t, "fn mk(b) { return {dep: fn(x) { b += x }, get: fn() => b} }\nconst acct = mk(100)\nacct.dep(5)")
	if len(snap.Closures) != 3 || len(snap.Cells) != 1 || len(snap.Cells[0].Refs) != 2 || snap.Cells[0].Value.Text != "105" {
		t.Fatalf("%s", jsonStr(snap))
	}
	var shared []int // mk 以外 (dep と get) が捕捉しているセル
	for _, c := range snap.Closures {
		if c.Func != "mk" {
			shared = append(shared, c.Free[0].Cell)
		}
	}
	if len(shared) != 2 || shared[0] != shared[1] {
		t.Errorf("closures should share the same cell id: %s", jsonStr(snap.Closures))
	}
}

func TestSnapshotFramesAndError(t *testing.T) {
	s, _ := Compile("fn f(x) {\n  return x + \"a\"\n}\nprint(f(1))")
	r := s.Step("run", 1000)
	if r.Status != "error" || r.Error.Line != 2 || r.Error.Col != 12 {
		t.Fatalf("%+v %+v", r, r.Error)
	}
	snap := s.VM.Snapshot()
	if len(snap.Frames) != 2 || snap.Frames[1].Func != "f" || snap.Frames[1].Locals[0].Name != "x" || snap.Frames[1].Locals[0].Value.Text != "1" {
		t.Fatalf("%s", jsonStr(snap.Frames))
	}
	if snap.Cur.Op != "ADD" || snap.Cur.Line != 2 {
		t.Errorf("cur %+v", snap.Cur)
	}
	roles := []string{}
	for _, sl := range snap.Stack {
		roles = append(roles, sl.Role)
	}
	// print, f (callee), x (local), ADD のオペランド 2 つが残っている
	if strings.Join(roles, ",") != "temp,callee,local,temp,temp" {
		t.Errorf("roles %v", roles)
	}
}

func TestSnapshotCyclicData(t *testing.T) {
	snap := runToEnd(t, "let a = [1]\npush(a, a)")
	for _, g := range snap.Globals {
		if g.Name == "a" && !strings.Contains(g.Value.Text, "[…]") {
			t.Errorf("cyclic array shown as %q", g.Value.Text)
		}
	}
}

func TestDisassembly(t *testing.T) {
	s, _ := Compile("fn add(a, b) => a + b\nprint(add(1, 2))")
	d := s.Disassembly()
	if len(d) != 2 || d[1].Name != "add" || d[1].LocalNames[1] != "b" {
		t.Fatalf("%s", jsonStr(d))
	}
	if d[1].Code[0].Op != "GET_LOCAL" || d[1].Code[0].Comment != "a" || d[1].Code[0].Line != 1 {
		t.Errorf("%+v", d[1].Code[0])
	}
}
