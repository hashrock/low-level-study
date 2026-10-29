package main

// デコーダ: 32 ビットの命令語を Inst 構造体に分解する。
// 実行 (cpu.go の execute) はこの Inst だけを見て動くので、
// 「ビットの切り出し」と「意味」がきれいに分かれる。

// Op は命令の種類。
type Op uint8

const (
	ILLEGAL Op = iota
	// RV64I: 上位即値・ジャンプ・分岐
	LUI
	AUIPC
	JAL
	JALR
	BEQ
	BNE
	BLT
	BGE
	BLTU
	BGEU
	// ロード・ストア
	LB
	LH
	LW
	LD
	LBU
	LHU
	LWU
	SB
	SH
	SW
	SD
	// 即値演算
	ADDI
	SLTI
	SLTIU
	XORI
	ORI
	ANDI
	SLLI
	SRLI
	SRAI
	ADDIW
	SLLIW
	SRLIW
	SRAIW
	// レジスタ演算
	ADD
	SUB
	SLL
	SLT
	SLTU
	XOR
	SRL
	SRA
	OR
	AND
	ADDW
	SUBW
	SLLW
	SRLW
	SRAW
	// M 拡張 (乗除算)
	MUL
	MULH
	MULHSU
	MULHU
	DIV
	DIVU
	REM
	REMU
	MULW
	DIVW
	DIVUW
	REMW
	REMUW
	// A 拡張 (アトミック)。幅 (.w/.d) は Inst.Width で区別
	LR
	SC
	AMOSWAP
	AMOADD
	AMOXOR
	AMOAND
	AMOOR
	AMOMIN
	AMOMAX
	AMOMINU
	AMOMAXU
	// システム・特権命令
	FENCE
	FENCE_I
	ECALL
	EBREAK
	MRET
	SRET
	WFI
	SFENCE_VMA
	// Zicsr
	CSRRW
	CSRRS
	CSRRC
	CSRRWI
	CSRRSI
	CSRRCI
	numOps
)

var opNames = [numOps]string{
	"illegal", "lui", "auipc", "jal", "jalr", "beq", "bne", "blt", "bge", "bltu", "bgeu",
	"lb", "lh", "lw", "ld", "lbu", "lhu", "lwu", "sb", "sh", "sw", "sd",
	"addi", "slti", "sltiu", "xori", "ori", "andi", "slli", "srli", "srai",
	"addiw", "slliw", "srliw", "sraiw",
	"add", "sub", "sll", "slt", "sltu", "xor", "srl", "sra", "or", "and",
	"addw", "subw", "sllw", "srlw", "sraw",
	"mul", "mulh", "mulhsu", "mulhu", "div", "divu", "rem", "remu",
	"mulw", "divw", "divuw", "remw", "remuw",
	"lr", "sc", "amoswap", "amoadd", "amoxor", "amoand", "amoor",
	"amomin", "amomax", "amominu", "amomaxu",
	"fence", "fence.i", "ecall", "ebreak", "mret", "sret", "wfi", "sfence.vma",
	"csrrw", "csrrs", "csrrc", "csrrwi", "csrrsi", "csrrci",
}

func (op Op) String() string { return opNames[op] }

// Inst はデコード済みの 1 命令。
type Inst struct {
	Op           Op
	Rd, Rs1, Rs2 uint32
	Imm          int64  // 符号拡張済みの即値 (CSR 命令では uimm)
	Csr          uint32 // CSR 番号
	Width        int    // A 拡張の幅 (4 or 8)
	Raw          uint32 // 元の命令語 (圧縮命令は展開後の 32 ビット)
	Size         uint64 // 命令長 (4 または圧縮命令なら 2)
	Compressed   uint32 // 圧縮命令の元の 16 ビット
}

// 即値の切り出し。int32 にしてから算術シフトすることで符号拡張する。
func immI(r uint32) int64 { return int64(int32(r) >> 20) }
func immS(r uint32) int64 { return int64(int32(r)>>25<<5) | int64(r>>7&0x1f) }
func immU(r uint32) int64 { return int64(int32(r & 0xfffff000)) }
func immB(r uint32) int64 {
	v := (r>>31&1)<<12 | (r>>7&1)<<11 | (r>>25&0x3f)<<5 | (r>>8&0xf)<<1
	return sext(v, 13)
}
func immJ(r uint32) int64 {
	v := (r>>31&1)<<20 | (r>>12&0xff)<<12 | (r>>20&1)<<11 | (r>>21&0x3ff)<<1
	return sext(v, 21)
}

// sext は下位 bits ビットの値を 64 ビットに符号拡張する。
func sext(v uint32, bits uint) int64 {
	shift := 64 - bits
	return int64(uint64(v)<<shift) >> shift
}

// Decode は 32 ビット命令をデコードする。知らない命令は Op=ILLEGAL。
func Decode(r uint32) Inst {
	in := Inst{Raw: r, Size: 4, Rd: r >> 7 & 0x1f, Rs1: r >> 15 & 0x1f, Rs2: r >> 20 & 0x1f}
	f3 := r >> 12 & 7
	f7 := r >> 25
	pick := func(table []Op, idx uint32) Op {
		if int(idx) < len(table) {
			return table[idx]
		}
		return ILLEGAL
	}

	switch r & 0x7f {
	case opLui:
		in.Op, in.Imm = LUI, immU(r)
	case opAuipc:
		in.Op, in.Imm = AUIPC, immU(r)
	case opJal:
		in.Op, in.Imm = JAL, immJ(r)
	case opJalr:
		if f3 == 0 {
			in.Op, in.Imm = JALR, immI(r)
		}
	case opBranch:
		in.Op = []Op{BEQ, BNE, ILLEGAL, ILLEGAL, BLT, BGE, BLTU, BGEU}[f3]
		in.Imm = immB(r)
	case opLoad:
		in.Op = []Op{LB, LH, LW, LD, LBU, LHU, LWU, ILLEGAL}[f3]
		in.Imm = immI(r)
	case opStore:
		in.Op = pick([]Op{SB, SH, SW, SD}, f3)
		in.Imm = immS(r)
	case opOpImm:
		in.Imm = immI(r)
		switch f3 {
		case 1: // slli: 上位 6 ビットが 0 のときのみ
			if r>>26 == 0 {
				in.Op, in.Imm = SLLI, int64(r>>20&0x3f)
			}
		case 5:
			switch r >> 26 {
			case 0x00:
				in.Op, in.Imm = SRLI, int64(r>>20&0x3f)
			case 0x10:
				in.Op, in.Imm = SRAI, int64(r>>20&0x3f)
			}
		default:
			in.Op = []Op{ADDI, ILLEGAL, SLTI, SLTIU, XORI, ILLEGAL, ORI, ANDI}[f3]
		}
	case opOpImm32:
		in.Imm = immI(r)
		shamt := int64(r >> 20 & 0x1f)
		switch {
		case f3 == 0:
			in.Op = ADDIW
		case f3 == 1 && f7 == 0:
			in.Op, in.Imm = SLLIW, shamt
		case f3 == 5 && f7 == 0:
			in.Op, in.Imm = SRLIW, shamt
		case f3 == 5 && f7 == 0x20:
			in.Op, in.Imm = SRAIW, shamt
		}
	case opOp:
		switch f7 {
		case 0x00:
			in.Op = []Op{ADD, SLL, SLT, SLTU, XOR, SRL, OR, AND}[f3]
		case 0x20:
			in.Op = []Op{SUB, ILLEGAL, ILLEGAL, ILLEGAL, ILLEGAL, SRA, ILLEGAL, ILLEGAL}[f3]
		case 0x01:
			in.Op = []Op{MUL, MULH, MULHSU, MULHU, DIV, DIVU, REM, REMU}[f3]
		}
	case opOp32:
		switch f7 {
		case 0x00:
			in.Op = []Op{ADDW, SLLW, ILLEGAL, ILLEGAL, ILLEGAL, SRLW, ILLEGAL, ILLEGAL}[f3]
		case 0x20:
			in.Op = []Op{SUBW, ILLEGAL, ILLEGAL, ILLEGAL, ILLEGAL, SRAW, ILLEGAL, ILLEGAL}[f3]
		case 0x01:
			in.Op = []Op{MULW, ILLEGAL, ILLEGAL, ILLEGAL, DIVW, DIVUW, REMW, REMUW}[f3]
		}
	case opAmo:
		if f3 == 2 || f3 == 3 {
			in.Width = 4 << (f3 - 2)
			amo := map[uint32]Op{0x02: LR, 0x03: SC, 0x01: AMOSWAP, 0x00: AMOADD, 0x04: AMOXOR,
				0x0c: AMOAND, 0x08: AMOOR, 0x10: AMOMIN, 0x14: AMOMAX, 0x18: AMOMINU, 0x1c: AMOMAXU}
			in.Op = amo[r>>27] // 無ければ 0 = ILLEGAL
		}
	case opMiscMem:
		in.Op = pick([]Op{FENCE, FENCE_I}, f3)
	case opSystem:
		in.Csr = r >> 20
		switch f3 {
		case 0:
			switch {
			case r == 0x00000073:
				in.Op = ECALL
			case r == 0x00100073:
				in.Op = EBREAK
			case r == 0x30200073:
				in.Op = MRET
			case r == 0x10200073:
				in.Op = SRET
			case r == 0x10500073:
				in.Op = WFI
			case f7 == 0x09 && in.Rd == 0:
				in.Op = SFENCE_VMA
			}
		case 4:
			// 予約
		default:
			in.Op = []Op{ILLEGAL, CSRRW, CSRRS, CSRRC, ILLEGAL, CSRRWI, CSRRSI, CSRRCI}[f3]
			in.Imm = int64(in.Rs1) // *I 形式では rs1 フィールドが 5 ビットの即値
		}
	}
	return in
}
