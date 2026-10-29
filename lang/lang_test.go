package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "examples/*.out を現在の出力で書き換える")

func run(src string) string {
	var buf bytes.Buffer
	if err := runSource(src, &buf, false); err != nil {
		buf.WriteString(err.Error() + "\n")
	}
	return buf.String()
}

// examples/*.lang を実行し、examples/*.out と出力を比較する。
func TestExamples(t *testing.T) {
	files, _ := filepath.Glob("examples/*.lang")
	if len(files) == 0 {
		t.Fatal("no examples found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			got := run(string(src))
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

// 言語仕様の要点を個別に確認する。want は出力に含まれるべき文字列。
func TestSemantics(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"no coercion +", `print(1 + "a")`, `1:9: runtime error: cannot apply + to number and string`},
		{"strict ==", `print(1 == "1", 1 == 1, nil == false)`, "false true false"},
		{"0 and empty string are truthy", `if (0) { print("a") } if ("") { print("b") }`, "a\nb"},
		{"missing key", `const m = {a: 1}; print(m.b)`, `key "b" not found`},
		{"out of range", `print([1, 2][5])`, "index 5 out of range"},
		{"block scope", `{ let x = 1 } print(x)`, "undefined variable x"},
		{"no implicit global", `y = 3`, "undefined variable y"},
		{"global const", `const c = 1; c = 2`, "cannot assign to const c"},
		{"local const", `fn f() { const k = 1; k = 2 }`, "compile error: cannot assign to const k"},
		{"redeclare", "let a = 1\nlet a = 2", "2:1: compile error: a is already declared"},
		{"division by zero", `print(1 / 0)`, "division by zero"},
		{"arity", `fn f(a, b) {} f(1)`, "f expects 2 argument(s) but got 1"},
		{"per-iteration binding", `let fs = []; for (i in range(3)) { push(fs, fn() => i) } for (f in fs) { print(f()) }`, "0\n1\n2"},
		{"shared closure cell", `fn mk() { let n = 0; return [fn() { n += 1 }, fn() => n] } const p = mk(); p[0](); p[0](); print(p[1]())`, "2"},
		{"nested capture", `fn a() { let x = 1; fn b() { fn c() { x += 1; return x } return c } return b()() } print(a())`, "2"},
		{"local recursion", `fn main() { fn fact(n) { if (n <= 1) { return 1 } return n * fact(n - 1) } return fact(10) } print(main())`, "3628800"},
		{"break continue", `let s = 0; let i = 0; while (true) { i += 1; if (i > 10) { break } if (i % 2 == 0) { continue } s += i } print(s)`, "25"},
		{"short circuit", `print(nil || "d", false && undefinedThing, 1 && 2)`, "d false 2"},
		{"string ops", `print("ab" + "cd", "a" < "b", len("日本語"), "日本語"[2])`, "abcd true 3 語"},
		{"numbers", `print(7 / 2, 7 % 3, 2 * -3)`, "3.5 1 -6"},
		{"map for-in keys", `for (k in {b: 1, a: 2}) { print(k) }`, "b\na"},
		{"type", `print(type(1), type("s"), type(nil), type([]), type({}), type(fn() {}), type(true))`, "number string nil array map function bool"},
		{"stack trace", "fn f(x) {\n  return x.k\n}\nf(1)", "2:11: runtime error: cannot index number\n    at f (2:11)\n    at <main> (4:2)"},
		{"syntax error pos", "let x = (1 +\n", "syntax error"},
		{"deep recursion", `fn r(n) { return r(n + 1) } r(0)`, "stack overflow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := run(c.src)
			if !strings.Contains(got, c.want) {
				t.Errorf("src: %s\n got: %q\nwant substring: %q", c.src, got, c.want)
			}
		})
	}
}

func TestDisassemble(t *testing.T) {
	var buf bytes.Buffer
	if err := runSource(`fn add(a, b) => a + b; print(add(1, 2))`, &buf, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CLOSURE", "DEF_GLOBAL", "== add", "GET_LOCAL", "ADD", "RETURN"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("disassembly missing %s:\n%s", want, buf.String())
		}
	}
}
