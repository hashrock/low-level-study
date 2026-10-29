package main

import (
	"encoding/binary"
	"testing"
)

// asmWords はソースをアセンブルして .text を 32bit 語の列で返す。
func asmWords(t *testing.T, src string) []uint32 {
	t.Helper()
	o, err := assemble("t.s", src)
	if err != nil {
		t.Fatalf("%q: %v", src, err)
	}
	text := o.section(".text")
	var ws []uint32
	for k := 0; k+4 <= len(text.data); k += 4 {
		ws = append(ws, binary.LittleEndian.Uint32(text.data[k:]))
	}
	return ws
}

// 期待値は RISC-V 仕様の定義どおりに手で組み立てた値 / GNU as の出力として
// よく知られている値。
func TestEncodeKnown(t *testing.T) {
	cases := []struct {
		src  string
		want uint32
	}{
		{"addi a0, zero, 1", 0x00100513},
		{"li a7, 93", 0x05d00893},
		{"addi sp, sp, -16", 0xff010113},
		{"add a0, a1, a2", 0x00c58533},
		{"sub a0, a1, a2", 0x40c58533},
		{"mul a0, a1, a2", 0x02c58533},
		{"divu a0, a1, a2", 0x02c5d533},
		{"remw a0, a1, a2", 0x02c5e53b},
		{"addiw a0, a0, 1", 0x0015051b},
		{"slli a0, a0, 32", 0x02051513},
		{"srai a0, a0, 63", 0x43f55513},
		{"sraiw a0, a0, 31", 0x41f5551b},
		{"lui a0, 0x12345", 0x12345537},
		{"auipc a0, 0", 0x00000517},
		{"ld ra, 8(sp)", 0x00813083},
		{"ld a0, -8(s0)", 0xff843503},
		{"lbu a0, 0(a1)", 0x0005c503},
		{"sd ra, 8(sp)", 0x00113423},
		{"sw a0, 0(sp)", 0x00a12023},
		{"sb t0, -1(x10)", 0xfe550fa3},
		{"ecall", 0x00000073},
		{"ebreak", 0x00100073},
		{"ret", 0x00008067},
		{"nop", 0x00000013},
		{"mv a0, a1", 0x00058513},
		{"not a0, a1", 0xfff5c513},
		{"neg a0, a1", 0x40b00533},
		{"seqz a0, a1", 0x0015b513},
		{"snez a0, a1", 0x00b03533},
		{"jr t0", 0x00028067},
		{"jalr t0", 0x000280e7},
		{"beq a0, a1, 8", 0x00b50463},
		{"beqz a0, 8", 0x00050463},
		{"bne a0, a1, -4", 0xfeb51ee3},
		{"jal ra, 0", 0x000000ef},
		{"j -8", 0xff9ff06f},
		{"fence", 0x0ff0000f},
		{"lui a0, %hi(0x12345678)", 0x12345537},
		{"addi a0, a0, %lo(0x12345678)", 0x67850513},
	}
	for _, c := range cases {
		ws := asmWords(t, c.src)
		if len(ws) != 1 || ws[0] != c.want {
			t.Errorf("%-28s got %08x want %08x", c.src, ws, c.want)
		}
	}
}

// ラベルへの分岐 (同一セクション内) はアセンブラが直接解決する。
func TestLocalBranch(t *testing.T) {
	ws := asmWords(t, `
top:
	nop
	bgt a0, a1, top     # blt a1, a0, -4
	j   end             # jal x0, +8
	nop
end:
	ble a0, a1, top     # bge a1, a0, -16
`)
	want := []uint32{0x00000013, 0xfea5cee3, 0x0080006f, 0x00000013, 0xfea5d8e3}
	for k := range want {
		if ws[k] != want[k] {
			t.Errorf("word %d: got %08x want %08x", k, ws[k], want[k])
		}
	}
}

// li の展開結果を小さな解釈器で実行し、正しい値が作られるか確かめる。
func TestLiAllValues(t *testing.T) {
	vals := []int64{0, 1, -1, 2047, -2048, 2048, -2049, 0x7ff, 0x800, 0x12345678,
		0x7fffffff, -0x80000000, 0x80000000, 0xffffffff, 0x100000000,
		0x123456789abcdef0, -0x123456789abcdef0, 0x7fffffffffffffff, -0x8000000000000000,
		0x0000ffff00000000, 0x7ff0000000000000, 0xdeadbeef}
	for _, v := range vals {
		seq := liSeq(10, v)
		if len(seq) > 8 {
			t.Errorf("li %#x: %d instructions", v, len(seq))
		}
		var x int64
		for _, in := range seq {
			imm := in.imm.add
			switch in.op {
			case "lui":
				x = int64(int32(uint32(imm) << 12))
			case "addi":
				if in.rs1 == 0 {
					x = imm
				} else {
					x += imm
				}
			case "addiw":
				x = int64(int32(x + imm))
			case "slli":
				x <<= uint(imm)
			default:
				t.Fatalf("unexpected op %s", in.op)
			}
			if _, err := encode(in, imm); err != nil {
				t.Errorf("li %#x: %s: %v", v, in.op, err)
			}
		}
		if x != v {
			t.Errorf("li %#x: got %#x (%d insts)", v, x, len(seq))
		}
	}
}

func TestErrors(t *testing.T) {
	bad := []string{
		"addi a0, a0, 4096",    // 即値が範囲外
		"slli a0, a0, 64",      // シフト量が範囲外
		"add a0, a1",           // オペランド不足
		"foo a0",               // 未知の命令
		"li a0, sym",           // li にシンボル
		"addi a0, a0, sym",     // %lo なしのシンボル
		"x: nop\nx: nop",       // 再定義
		".bss\n.byte 1",        // .bss に非ゼロ
		"lui a0, %lo(sym)",     // 組み合わせ誤り
		"beq a0, a1, 4097",     // 奇数・範囲外
		".section .weird",      // 未対応セクション
		"addi a0, a0, 1 ; nop", // ';' 非対応
	}
	for _, src := range bad {
		if _, err := assemble("t.s", src); err == nil {
			t.Errorf("%q: expected error", src)
		}
	}
}

func TestDirectives(t *testing.T) {
	o, err := assemble("t.s", `
	.equ K, 3*4+1
	.data
d:	.byte 1, 'A', -1
	.half 0x1234
	.align 2
	.word K, d+4
	.dword -2
	.asciz "hi\n"
	.zero 2
`)
	if err != nil {
		t.Fatal(err)
	}
	d := o.section(".data")
	want := []byte{1, 'A', 0xff, 0x34, 0x12, 0, 0, 0, // .align 2 で 8 に
		13, 0, 0, 0, 0, 0, 0, 0, // K, d+4 (リロケーションで埋まる)
		0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 'h', 'i', '\n', 0, 0, 0}
	if string(d.data) != string(want) {
		t.Errorf("data = % x\nwant   % x", d.data, want)
	}
	if len(d.relocs) != 1 || d.relocs[0] != (reloc{off: 12, typ: r32, sym: "d", addend: 4}) {
		t.Errorf("relocs = %+v", d.relocs)
	}
}
