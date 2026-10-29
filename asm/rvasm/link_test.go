package rvasm

import (
	"bytes"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildFiles は cmdBuild と同じ経路 (as → .o バイト列 → 読み直し → ld) で実行ファイルを作る。
func buildFiles(t *testing.T, paths ...string) []byte {
	t.Helper()
	var objs []*object
	for _, p := range paths {
		o, err := assembleFile(p)
		if err != nil {
			t.Fatal(err)
		}
		b, err := writeObject(o)
		if err != nil {
			t.Fatal(err)
		}
		checkObject(t, b)
		if o, err = readObject(p, b); err != nil {
			t.Fatal(err)
		}
		objs = append(objs, o)
	}
	exe, err := link(objs)
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func assembleFile(path string) (*object, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return assemble(path, string(src))
}

// checkObject は .o が debug/elf で読める正しい ET_REL であることを確かめる。
func checkObject(t *testing.T, b []byte) {
	t.Helper()
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != elf.ET_REL || f.Machine != elf.EM_RISCV {
		t.Errorf("bad object header: %v %v", f.Type, f.Machine)
	}
	if _, err := f.Symbols(); err != nil {
		t.Errorf("symbols: %v", err)
	}
	for _, s := range f.Sections {
		if s.Type == elf.SHT_RELA && f.Sections[s.Link].Type != elf.SHT_SYMTAB {
			t.Errorf("%s: sh_link does not point to .symtab", s.Name)
		}
	}
}

func checkExec(t *testing.T, b []byte) *elf.File {
	t.Helper()
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != elf.ET_EXEC || f.Machine != elf.EM_RISCV || f.Class != elf.ELFCLASS64 {
		t.Fatalf("bad header %v %v %v", f.Type, f.Machine, f.Class)
	}
	syms, _ := f.Symbols()
	var start uint64
	for _, s := range syms {
		if s.Name == "_start" {
			start = s.Value
		}
	}
	if f.Entry != start || start == 0 {
		t.Errorf("entry %#x != _start %#x", f.Entry, start)
	}
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		if p.Vaddr%0x1000 != p.Off%0x1000 {
			t.Errorf("segment vaddr/offset not congruent: %+v", p.ProgHeader)
		}
		if p.Flags&elf.PF_W != 0 && p.Flags&elf.PF_X != 0 {
			t.Errorf("W+X segment")
		}
		if p.Flags&elf.PF_X != 0 && (f.Entry < p.Vaddr || f.Entry >= p.Vaddr+p.Memsz) {
			t.Errorf("entry outside text segment")
		}
	}
	if f.Progs[0].Vaddr != baseAddr {
		t.Errorf("first segment at %#x", f.Progs[0].Vaddr)
	}
	return f
}

func TestExamples(t *testing.T) {
	fib := ""
	a, b := uint64(0), uint64(1)
	for k := 0; k <= 50; k++ {
		fib += fmt.Sprintf("%d\n", a)
		a, b = b, a+b
	}
	fib += "6765\n"
	cases := []struct {
		files []string
		want  string
	}{
		{[]string{"hello.s"}, "Hello, RISC-V!\n"},
		{[]string{"main.s", "lib.s"}, "main.s calls puts() in lib.s\n7\n6\n1000000007\n"},
		{[]string{"lib.s", "main.s"}, "main.s calls puts() in lib.s\n7\n6\n1000000007\n"},
		{[]string{"fib.s"}, fib},
	}
	for _, c := range cases {
		var paths []string
		for _, f := range c.files {
			paths = append(paths, filepath.Join("..", "examples", f))
		}
		exe := buildFiles(t, paths...)
		f := checkExec(t, exe)
		out, code, err := runELF(f, 10_000_000)
		if err != nil {
			t.Errorf("%v: %v", c.files, err)
			continue
		}
		if out != c.want || code != 0 {
			t.Errorf("%v: exit %d output:\n%s\nwant:\n%s", c.files, code, out, c.want)
		}
	}
}

// オブジェクトファイル経由のリロケーション (別ファイル・別セクション) を確認する。
func TestCrossFileRelocs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(src), 0o644)
		return p
	}
	a := write("a.s", `
	.globl _start
_start:
	lui  t0, %hi(val)
	ld   a0, %lo(val)(t0)       # R_RISCV_HI20 / LO12_I
	lui  t1, %hi(out)
	sd   a0, %lo(out)(t1)       # LO12_S
.Lp:	auipc t2, %pcrel_hi(out)
	ld   a1, %pcrel_lo(.Lp)(t2)
	bne  a0, a1, fail           # 同一セクション: アセンブラが解決
	tail finish                 # 別ファイル
fail:
	li a0, 1
	li a7, 93
	ecall
`)
	b := write("b.s", `
	.data
	.globl val
val:	.dword 42
	.bss
	.globl out
out:	.zero 8
	.text
	.globl finish
finish:
	li   t0, 42
	bne  a0, t0, bad
	bne  a1, t0, bad
	li   a0, 0
	li   a7, 93
	ecall
bad:	li a0, 3
	li   a7, 93
	ecall
`)
	exe := buildFiles(t, a, b)
	f := checkExec(t, exe)
	if _, code, err := runELF(f, 1000); err != nil || code != 0 {
		t.Errorf("code=%d err=%v", code, err)
	}
}

func TestUndefinedAndDuplicate(t *testing.T) {
	o1, _ := assemble("a.s", ".globl _start\n_start: call nowhere")
	if _, err := link([]*object{o1}); err == nil {
		t.Error("expected undefined symbol error")
	}
	o2, _ := assemble("b.s", ".globl _start\n_start: nop")
	o3, _ := assemble("c.s", ".globl _start\n_start: nop")
	if _, err := link([]*object{o2, o3}); err == nil {
		t.Error("expected duplicate symbol error")
	}
}

// ---- テスト用の最小 RV64IM 解釈器 ----
// エミュレータが無い環境でも、生成した実行ファイルが実際に動くことを確かめるため。

func runELF(f *elf.File, maxSteps int) (string, int, error) {
	mem := map[uint64]byte{}
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		data := make([]byte, p.Filesz)
		p.ReadAt(data, 0)
		for k := uint64(0); k < p.Memsz; k++ {
			mem[p.Vaddr+k] = 0
			if k < p.Filesz {
				mem[p.Vaddr+k] = data[k]
			}
		}
	}
	const stackTop = 0x7fff0000
	for k := uint64(stackTop - 0x10000); k < stackTop; k++ {
		mem[k] = 0
	}
	var x [32]uint64
	x[2] = stackTop
	pc := f.Entry
	var out bytes.Buffer
	load := func(addr uint64, n int) (uint64, error) {
		var v uint64
		for k := n - 1; k >= 0; k-- {
			b, ok := mem[addr+uint64(k)]
			if !ok {
				return 0, fmt.Errorf("load fault at %#x (pc=%#x)", addr, pc)
			}
			v = v<<8 | uint64(b)
		}
		return v, nil
	}
	store := func(addr uint64, n int, v uint64) error {
		for k := 0; k < n; k++ {
			if _, ok := mem[addr+uint64(k)]; !ok {
				return fmt.Errorf("store fault at %#x (pc=%#x)", addr, pc)
			}
			mem[addr+uint64(k)] = byte(v >> (8 * k))
		}
		return nil
	}
	sx := func(v uint64, bits uint) uint64 { return uint64(signExtend(int64(v), bits)) }
	for step := 0; step < maxSteps; step++ {
		w64, err := load(pc, 4)
		if err != nil {
			return out.String(), -1, err
		}
		w := uint32(w64)
		op, rd, f3 := w&0x7f, w>>7&31, w>>12&7
		rs1, rs2, f7 := x[w>>15&31], x[w>>20&31], w>>25
		immI := sx(uint64(w>>20), 12)
		immS := sx(uint64(w>>25<<5|w>>7&31), 12)
		immB := sx(uint64(w>>31<<12|(w>>7&1)<<11|(w>>25&0x3f)<<5|(w>>8&0xf)<<1), 13)
		immU := sx(uint64(w&0xfffff000), 32)
		immJ := sx(uint64(w>>31<<20|(w>>12&0xff)<<12|(w>>20&1)<<11|(w>>21&0x3ff)<<1), 21)
		next := pc + 4
		var res uint64
		write := true
		alu := func(a, b uint64, f3, f7 uint32, word bool) uint64 {
			sh := b & 63
			if word {
				sh = b & 31
			}
			if f7 == 1 { // M 拡張
				if word {
					a, b = sx(a, 32), sx(b, 32)
				}
				switch f3 {
				case 0:
					return a * b
				case 4:
					if word {
						return uint64(int32(a) / int32(b))
					}
					return uint64(int64(a) / int64(b))
				case 5:
					if word {
						return uint64(uint32(a) / uint32(b))
					}
					return a / b
				case 6:
					if word {
						return uint64(int32(a) % int32(b))
					}
					return uint64(int64(a) % int64(b))
				case 7:
					if word {
						return uint64(uint32(a) % uint32(b))
					}
					return a % b
				}
				panic("mulh not supported in test interpreter")
			}
			switch f3 {
			case 0:
				if f7 == 0x20 {
					return a - b
				}
				return a + b
			case 1:
				return a << sh
			case 2:
				if int64(a) < int64(b) {
					return 1
				}
				return 0
			case 3:
				if a < b {
					return 1
				}
				return 0
			case 4:
				return a ^ b
			case 5:
				if word {
					if f7&0x20 != 0 {
						return uint64(int32(a) >> sh)
					}
					return uint64(uint32(a) >> sh)
				}
				if f7&0x20 != 0 {
					return uint64(int64(a) >> sh)
				}
				return a >> sh
			case 6:
				return a | b
			}
			return a & b
		}
		switch op {
		case opLUI:
			res = immU
		case opAUIPC:
			res = pc + immU
		case opJAL:
			res, next = pc+4, pc+immJ
		case opJALR:
			res, next = pc+4, (rs1+immI)&^1
		case 0x63:
			write = false
			taken := map[uint32]bool{0: rs1 == rs2, 1: rs1 != rs2, 4: int64(rs1) < int64(rs2),
				5: int64(rs1) >= int64(rs2), 6: rs1 < rs2, 7: rs1 >= rs2}[f3]
			if taken {
				next = pc + immB
			}
		case 0x03:
			n := []int{1, 2, 4, 8, 1, 2, 4}[f3]
			v, err := load(rs1+immI, n)
			if err != nil {
				return out.String(), -1, err
			}
			res = v
			if f3 < 4 {
				res = sx(v, uint(8*n))
			}
		case 0x23:
			write = false
			if err := store(rs1+immS, 1<<f3, rs2); err != nil {
				return out.String(), -1, err
			}
		case opIMM:
			f7i := uint32(0)
			if f3 == 5 {
				f7i = f7 & 0x20
			}
			res = alu(rs1, immI, f3, f7i, false)
		case opIMM32:
			f7i := uint32(0)
			if f3 == 5 {
				f7i = f7 & 0x20
			}
			res = sx(alu(rs1, immI, f3, f7i, true), 32)
		case opOP:
			res = alu(rs1, rs2, f3, f7, false)
		case opOP32:
			res = sx(alu(rs1, rs2, f3, f7, true), 32)
		case opSYSTEM:
			write = false
			switch x[17] {
			case 64: // write
				for k := uint64(0); k < x[12]; k++ {
					b, _ := load(x[11]+k, 1)
					out.WriteByte(byte(b))
				}
				x[10] = x[12]
			case 93: // exit
				return out.String(), int(x[10]), nil
			default:
				return out.String(), -1, fmt.Errorf("unknown syscall %d", x[17])
			}
		default:
			return out.String(), -1, fmt.Errorf("illegal instruction %08x at %#x", w, pc)
		}
		if write && rd != 0 {
			x[rd] = res
		}
		pc = next
	}
	return out.String(), -1, fmt.Errorf("step limit exceeded")
}
