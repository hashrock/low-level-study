package rvasm

import (
	"bytes"
	"debug/elf"
	"errors"
	"testing"
)

// ファイルを使わずにメモリ上だけでアセンブル + リンクし、結果を debug/elf と
// リスティング / リロケーション情報で確かめる。
func TestBuildInMemory(t *testing.T) {
	mainS := `
	.globl _start
_start:
	li   a0, 1
	call twice          # 別ファイル: R_RISCV_CALL
	la   t0, result
	sd   a0, 0(t0)
	beq  a0, zero, _start
	li   a7, 93
	ecall
	.bss
result:	.zero 8
`
	libS := `
	.globl twice
twice:
	slli a0, a0, 1
	ret
	.rodata
msg:	.asciz "hi"
`
	p, err := BuildProgram(Source{"main.s", mainS}, Source{"lib.s", libS})
	if err != nil {
		t.Fatal(err)
	}
	f, err := elf.NewFile(bytes.NewReader(p.ELF))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != elf.ET_EXEC || f.Machine != elf.EM_RISCV || f.Entry != p.Entry {
		t.Fatalf("bad ELF header: %v %v %#x", f.Type, f.Machine, f.Entry)
	}
	syms, _ := f.Symbols()
	addr := map[string]uint64{}
	for _, s := range syms {
		addr[s.Name] = s.Value
	}
	if addr["_start"] != f.Entry || addr["twice"] == 0 {
		t.Fatalf("symbols: %v", addr)
	}
	text := f.Section(".text")
	code, _ := text.Data()

	// リスティング: 命令語は ELF の .text と一致し、Decode で分解されている
	var insts []Line
	for _, l := range p.Listing {
		if l.Kind != "inst" {
			continue
		}
		insts = append(insts, l)
		off := l.Addr - text.Addr
		if w := uint32(code[off]) | uint32(code[off+1])<<8 | uint32(code[off+2])<<16 | uint32(code[off+3])<<24; w != l.Word {
			t.Errorf("%#x: listing word %#08x != ELF %#08x", l.Addr, l.Word, w)
		}
	}
	first := insts[0]
	if first.Source != "li   a0, 1" || first.Line != 4 || first.Pseudo != "li" || first.Mnemonic != "addi" || first.Word != 0x00100513 {
		t.Errorf("first: %+v", first)
	}
	if e := first.Enc; e.Format != "I" || e.Rd != 10 || e.Rs1 != 0 || e.Imm != 1 || e.Opcode != 0x13 {
		t.Errorf("first enc: %+v", e)
	}
	var sawB, sawJalr, sawShift bool
	for _, l := range insts {
		switch l.Mnemonic {
		case "beq":
			sawB = true
			if l.Enc.Format != "B" || l.Addr+uint64(l.Enc.Imm) != addr["_start"] {
				t.Errorf("beq: %+v", l.Enc)
			}
		case "jalr":
			sawJalr = sawJalr || l.File == "main.s" // call の後半
		case "slli":
			sawShift = l.Enc.Imm == 1 && l.Enc.Fields[0].Name == "funct6"
		}
		hi := 32
		for _, fl := range l.Enc.Fields { // フィールドは 31→0 を隙間なく覆う
			if fl.Hi != hi-1 || fl.Lo > fl.Hi {
				t.Errorf("%s: bad field layout %+v", l.Source, l.Enc.Fields)
			}
			hi = fl.Lo
		}
		if hi != 0 {
			t.Errorf("%s: fields do not reach bit 0", l.Source)
		}
	}
	if !sawB || !sawJalr || !sawShift {
		t.Errorf("missing instructions: beq=%v jalr=%v slli=%v", sawB, sawJalr, sawShift)
	}

	// リロケーション: call twice は S - P に解決されている
	var call *Reloc
	for k, r := range p.Relocs {
		if r.Type == "R_RISCV_CALL_PLT" || r.Type == "R_RISCV_CALL" {
			call = &p.Relocs[k]
		}
	}
	if call == nil || call.Symbol != "twice" || call.Line != 5 || call.SymAddr != addr["twice"] ||
		call.Value != int64(addr["twice"])-int64(call.Addr) || call.Before == call.After {
		t.Errorf("call reloc: %+v", call)
	}
}

func TestErrorPositions(t *testing.T) {
	var e *Error
	_, err := Assemble("bad.s", "  nop\n  addi a0, a0\n")
	if !errors.As(err, &e) || e.File != "bad.s" || e.Line != 2 || e.Col != 3 {
		t.Errorf("assemble error: %v", err)
	}
	_, err = Assemble("bad.s", "nop\n  li a0, \"x\x01")
	if !errors.As(err, &e) || e.Line != 2 || e.Col != 10 {
		t.Errorf("lex error: %#v", err)
	}
	_, err = Build(Source{"u.s", ".globl _start\n_start:\n    call nowhere\n"})
	if !errors.As(err, &e) || e.File != "u.s" || e.Line != 3 || e.Col != 5 {
		t.Errorf("link error: %v", err)
	}
}
