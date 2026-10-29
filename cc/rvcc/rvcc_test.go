package rvcc

import (
	"bytes"
	"debug/elf"
	"errors"
	"strings"
	"testing"
)

func TestErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{"int main() { return x; }", "undeclared identifier x"},
		{"int main() { int a; a.b = 1; }", "member access on non-struct"},
		{"struct s { int a; }; int main() { struct s v; return v.b; }", "no member named b"},
		{"int main() { int a; return *a; }", "cannot dereference int"},
		{"int f(int a); int main() { return f(1, 2); }", "wrong number of arguments"},
		{"int main() { 1 = 2; }", "cannot assign"},
		{"int main() { break; }", "break outside"},
		{"void f() { return 1; }", "void function returns a value"},
		{"int main() { unsigned x; }", "unsigned types are not supported"},
		{"int main() { return 1 }", `expected ";"`},
		{"int main() { int *p; long q; return p * q; }", "invalid operands to *"},
		{"int a[x];", "undeclared identifier x"},
	}
	for _, c := range cases {
		_, err := Compile("t.c", c.src, 2)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.src, err, c.want)
		}
	}
}

// エラーは行・桁つきの *Error で返る。
func TestErrorPosition(t *testing.T) {
	var e *Error
	_, err := Compile("t.c", "int main() {\n  /* x\n */ return y;\n}\n", 2)
	if !errors.As(err, &e) || e.File != "t.c" || e.Line != 3 || e.Col != 12 {
		t.Errorf("got %#v", err)
	}
	_, err = Compile("t.c", "int main() {\n\treturn 1 @ 2;\n}", 2)
	if !errors.As(err, &e) || e.Line != 2 || e.Col != 11 {
		t.Errorf("got %#v", err)
	}
}

// コンパイル → アセンブル → リンクをファイルを使わずにメモリ上だけで行う。
func TestBuildInMemory(t *testing.T) {
	src := "int sq(int x) {\n  return x * x;\n}\nint main() {\n  printf(\"%d\\n\", sq(7));\n  return 0;\n}\n"
	out, err := Compile("sq.c", src, 1)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.Asm, "\n"), "\n")
	if len(lines) != len(out.Lines) {
		t.Fatalf("Lines has %d entries for %d asm lines", len(out.Lines), len(lines))
	}
	for k, l := range lines {
		if strings.Contains(l, "mulw") && out.Lines[k] != 2 {
			t.Errorf("mulw maps to C line %d, want 2", out.Lines[k])
		}
		if l == "sq:" && out.Lines[k] != 1 {
			t.Errorf("sq: maps to C line %d, want 1", out.Lines[k])
		}
	}

	exe, err := Build([]Source{{Name: "sq.c", Text: src}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := elf.NewFile(bytes.NewReader(exe))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != elf.ET_EXEC || f.Machine != elf.EM_RISCV {
		t.Fatalf("bad header %v %v", f.Type, f.Machine)
	}
	syms, _ := f.Symbols()
	want := map[string]bool{"_start": false, "main": false, "sq": false, "printf": false}
	for _, s := range syms {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
			if s.Name == "_start" && s.Value != f.Entry {
				t.Errorf("entry %#x != _start %#x", f.Entry, s.Value)
			}
		}
	}
	for n, ok := range want {
		if !ok {
			t.Errorf("symbol %s missing", n)
		}
	}
	crt0, libc, hdr := Libc()
	if !strings.Contains(crt0, "_start") || !strings.Contains(libc, "printf") || !strings.Contains(hdr, "printf") {
		t.Error("Libc() sources look wrong")
	}
}
