package main

import "encoding/binary"

// テスト用のミニアセンブラと ELF ビルダ。asm/ プロジェクトに依存せずに
// テストプログラムを作るためのもの。

// レジスタ番号 (ABI 名)
const (
	zero = 0
	ra   = 1
	sp   = 2
	t0   = 5
	t1   = 6
	t2   = 7
	a0   = 10
	a1   = 11
	a2   = 12
	a3   = 13
	a7   = 17
)

// 命令エンコード関数 (よく使うものだけ)
func addi(rd, rs1 uint32, imm int64) uint32 { return encI(opOpImm, rd, 0, rs1, imm) }
func add(rd, rs1, rs2 uint32) uint32        { return encR(opOp, rd, 0, rs1, rs2, 0) }
func ld(rd, rs1 uint32, imm int64) uint32   { return encI(opLoad, rd, 3, rs1, imm) }
func sd(rs2, rs1 uint32, imm int64) uint32  { return encS(opStore, 3, rs1, rs2, imm) }
func lui(rd uint32, imm int64) uint32       { return encU(opLui, rd, imm) }
func csrrw(rd, csr, rs1 uint32) uint32      { return encI(opSystem, rd, 1, rs1, int64(csr)) }
func csrrs(rd, csr, rs1 uint32) uint32      { return encI(opSystem, rd, 2, rs1, int64(csr)) }
func csrr(rd, csr uint32) uint32            { return csrrs(rd, csr, 0) }
func csrw(csr, rs1 uint32) uint32           { return csrrw(0, csr, rs1) }

const (
	ecall = 0x00000073
	mret  = 0x30200073
	wfi   = 0x10500073
)

// Asm は ラベル付きで命令を並べ、最後にアドレスを解決する簡易アセンブラ。
type Asm struct {
	base   uint64
	words  []uint32
	data   []byte
	labels map[string]uint64 // コードのラベルは word 番号*4、データは "data:" 接頭辞
	fixups []func()
}

func NewAsm(base uint64) *Asm { return &Asm{base: base, labels: map[string]uint64{}} }

func (a *Asm) pc() uint64        { return a.base + uint64(4*len(a.words)) }
func (a *Asm) Emit(ws ...uint32) { a.words = append(a.words, ws...) }
func (a *Asm) Label(name string) { a.labels[name] = a.pc() }
func (a *Asm) Data(name string, b []byte) {
	a.labels["data:"+name] = uint64(len(a.data))
	a.data = append(a.data, b...)
}

func (a *Asm) addr(name string) uint64 {
	if off, ok := a.labels["data:"+name]; ok {
		return a.base + uint64(4*len(a.words)) + off // データはコードの直後に置かれる
	}
	if v, ok := a.labels[name]; ok {
		return v
	}
	panic("undefined label " + name)
}

// later は位置 idx の命令を、全ラベル確定後に f の結果で置き換える。
func (a *Asm) later(f func(pc uint64) []uint32, n int) {
	idx, pc := len(a.words), a.pc()
	a.words = append(a.words, make([]uint32, n)...)
	a.fixups = append(a.fixups, func() { copy(a.words[idx:], f(pc)) })
}

// Branch: f3 = 0 beq, 1 bne, 4 blt, 5 bge, 6 bltu, 7 bgeu
func (a *Asm) Branch(f3, rs1, rs2 uint32, label string) {
	a.later(func(pc uint64) []uint32 { return []uint32{encB(f3, rs1, rs2, int64(a.addr(label)-pc))} }, 1)
}

func (a *Asm) Jal(rd uint32, label string) {
	a.later(func(pc uint64) []uint32 { return []uint32{encJ(rd, int64(a.addr(label)-pc))} }, 1)
}

// La: rd = ラベルのアドレス (auipc + addi)
func (a *Asm) La(rd uint32, label string) {
	a.later(func(pc uint64) []uint32 {
		off := int64(a.addr(label) - pc)
		hi := (off + 0x800) &^ 0xfff
		return []uint32{encU(opAuipc, rd, hi), addi(rd, rd, off-hi)}
	}, 2)
}

// Li: rd = 32 ビット符号付き定数 (lui + addi)
func (a *Asm) Li(rd uint32, v int64) {
	hi := (v + 0x800) &^ 0xfff
	if hi != 0 {
		a.Emit(lui(rd, hi), encI(opOpImm32, rd, 0, rd, v-hi)) // addiw
	} else {
		a.Emit(addi(rd, zero, v))
	}
}

// Bytes は命令列 + データのバイト列を返す。
func (a *Asm) Bytes() []byte {
	for _, f := range a.fixups {
		f()
	}
	out := make([]byte, 4*len(a.words))
	for i, w := range a.words {
		binary.LittleEndian.PutUint32(out[4*i:], w)
	}
	return append(out, a.data...)
}

// BuildELF は最小の ELF64 を作る。
//
//	0x00 ELF ヘッダ (64 バイト)
//	0x40 プログラムヘッダ x2 (56 バイトずつ)
//	0xb0 コード + データ
//
// セグメント 1: ファイル先頭から全部 (RWX)。コードはヘッダの直後に置かれる。
// セグメント 2: bssSize > 0 なら、次のページに filesz=0 の .bss だけのセグメント。
const elfHeaderSize = 64 + 2*56

func BuildELF(vaddr uint64, body []byte, bssSize uint64) []byte {
	le := binary.LittleEndian
	h := make([]byte, elfHeaderSize)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1}) // 64bit, リトルエンディアン, version 1
	le.PutUint16(h[16:], 2)                       // e_type = ET_EXEC
	le.PutUint16(h[18:], 243)                     // e_machine = EM_RISCV
	le.PutUint32(h[20:], 1)                       // e_version
	le.PutUint64(h[24:], vaddr+elfHeaderSize)     // e_entry
	le.PutUint64(h[32:], 64)                      // e_phoff
	le.PutUint16(h[52:], 64)                      // e_ehsize
	le.PutUint16(h[54:], 56)                      // e_phentsize
	size := uint64(elfHeaderSize + len(body))
	phdr := func(p []byte, flags uint32, va, filesz, memsz uint64) {
		le.PutUint32(p[0:], 1) // p_type = PT_LOAD
		le.PutUint32(p[4:], flags)
		le.PutUint64(p[16:], va) // p_vaddr
		le.PutUint64(p[24:], va) // p_paddr
		le.PutUint64(p[32:], filesz)
		le.PutUint64(p[40:], memsz)
		le.PutUint64(p[48:], 0x1000) // p_align
	}
	phdr(h[64:], 7, vaddr, size, size)
	le.PutUint16(h[56:], 1) // e_phnum
	if bssSize > 0 {
		phdr(h[120:], 6, pageUp(vaddr+size), 0, bssSize)
		le.PutUint16(h[56:], 2)
	}
	return append(h, body...)
}

// userProgram は vaddr 0x10000 から始まるユーザプログラム用の Asm を返す。
func userProgram() *Asm { return NewAsm(0x10000 + elfHeaderSize) }

// sysExit: exit(a0)
func (a *Asm) Exit(code int64) {
	a.Emit(addi(a0, zero, code), addi(a7, zero, sysExit), ecall)
}
