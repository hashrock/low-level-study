package minicc

import (
	"bytes"
	"debug/elf"
	"errors"
	"strings"
	"testing"
)

// コンパイル → アセンブル → リンクをファイルを使わずにメモリ上だけで行う。
func TestBuildInMemory(t *testing.T) {
	src := "let x = 1\nfn add(a, b) {\n  return a + b\n}\nprint(add(x, 2))\n"
	out, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.Asm, "\n"), "\n")
	if len(lines) != len(out.Lines) {
		t.Fatalf("Lines has %d entries for %d asm lines", len(out.Lines), len(lines))
	}
	var sawAdd, sawFn bool
	for k, l := range lines {
		if strings.Contains(l, "rt_add") || strings.HasPrefix(l, "\tadd ") {
			sawAdd = sawAdd || out.Lines[k] == 3
		}
		if strings.HasPrefix(l, "fn_") && strings.HasSuffix(l, ":") {
			sawFn = out.Lines[k] == 2
		}
	}
	if !sawAdd || !sawFn {
		t.Errorf("line map: add on line 3 = %v, fn label on line 2 = %v\n%s", sawAdd, sawFn, out.Asm)
	}

	exe, err := Build("add.lang", src)
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
	found := map[string]bool{}
	for _, s := range syms {
		found[s.Name] = true
		if s.Name == "_start" && s.Value != f.Entry {
			t.Errorf("entry %#x != _start %#x", f.Entry, s.Value)
		}
	}
	if !found["_start"] || !found["mini_main"] {
		t.Errorf("missing symbols: %v", found)
	}
	if !strings.Contains(Runtime(), "_start") {
		t.Error("Runtime() looks wrong")
	}
}

func TestErrorPosition(t *testing.T) {
	var e *LangError
	_, err := Compile("let a = 1\nprint(  y)\n")
	if !errors.As(err, &e) || e.Pos.Line != 2 || e.Pos.Col != 9 {
		t.Errorf("got %#v", err)
	}
	_, err = Compile("let a = (1\n")
	if !errors.As(err, &e) || e.Kind != "syntax error" || e.Pos.Line == 0 {
		t.Errorf("got %#v", err)
	}
}
