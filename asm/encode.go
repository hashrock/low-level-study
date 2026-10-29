package main

// encode.go — 命令表と 32bit 命令語へのエンコード。
//
// RV64IM の全命令は 6 つの基本形式 (R/I/S/B/U/J) のどれかに属する。
// 命令ごとに「形式・固定ビット (opcode/funct3/funct7)・オペランドの並び」を
// 表 (opTable) に書いておき、エンコードは
//
//	word = 固定ビット | rd<<7 | rs1<<15 | rs2<<20 | 即値ビット(形式ごと)
//
// という 1 つの式で行う。即値の詰め方 (immBits) だけが形式ごとに異なる。
// この immBits はリンカがリロケーションを適用するときにも再利用する。

import "fmt"

type format int

const (
	fR      format = iota // rd, rs1, rs2
	fI                    // rd, rs1, imm[11:0]
	fShift                // I 形式のシフト (shamt 6bit)
	fShiftW               // 32bit 版シフト (shamt 5bit)
	fS                    // rs1, rs2, imm[11:0] (store)
	fB                    // rs1, rs2, imm[12:1] (branch)
	fU                    // rd, imm[31:12]
	fJ                    // rd, imm[20:1]
	fSys                  // オペランドなし (ecall 等)
)

type opInfo struct {
	f     format
	base  uint32 // opcode / funct3 / funct7 を埋めた固定ビット
	shape string // オペランドの並び: d=rd s=rs1 t=rs2 i=即値 m=即値(rs1)
}

func r(opc, f3, f7 uint32) opInfo { return opInfo{fR, f7<<25 | f3<<12 | opc, "d,s,t"} }
func i(opc, f3 uint32) opInfo     { return opInfo{fI, f3<<12 | opc, "d,s,i"} }
func ld(f3 uint32) opInfo         { return opInfo{fI, f3<<12 | 0x03, "d,m"} }
func st(f3 uint32) opInfo         { return opInfo{fS, f3<<12 | 0x23, "t,m"} }
func br(f3 uint32) opInfo         { return opInfo{fB, f3<<12 | 0x63, "s,t,i"} }
func sh(opc, f3, f7 uint32, f format) opInfo {
	return opInfo{f, f7<<25 | f3<<12 | opc, "d,s,i"}
}

const (
	opOP     = 0x33 // R 形式の演算
	opOP32   = 0x3b // 32bit 演算 (addw など)
	opIMM    = 0x13 // 即値演算
	opIMM32  = 0x1b // 32bit 即値演算
	opLUI    = 0x37
	opAUIPC  = 0x17
	opJAL    = 0x6f
	opJALR   = 0x67
	opSYSTEM = 0x73
)

var opTable = map[string]opInfo{
	// RV64I 整数演算 (R)
	"add": r(opOP, 0, 0), "sub": r(opOP, 0, 0x20), "sll": r(opOP, 1, 0),
	"slt": r(opOP, 2, 0), "sltu": r(opOP, 3, 0), "xor": r(opOP, 4, 0),
	"srl": r(opOP, 5, 0), "sra": r(opOP, 5, 0x20), "or": r(opOP, 6, 0), "and": r(opOP, 7, 0),
	"addw": r(opOP32, 0, 0), "subw": r(opOP32, 0, 0x20), "sllw": r(opOP32, 1, 0),
	"srlw": r(opOP32, 5, 0), "sraw": r(opOP32, 5, 0x20),
	// M 拡張 (funct7 = 1)
	"mul": r(opOP, 0, 1), "mulh": r(opOP, 1, 1), "mulhsu": r(opOP, 2, 1), "mulhu": r(opOP, 3, 1),
	"div": r(opOP, 4, 1), "divu": r(opOP, 5, 1), "rem": r(opOP, 6, 1), "remu": r(opOP, 7, 1),
	"mulw": r(opOP32, 0, 1), "divw": r(opOP32, 4, 1), "divuw": r(opOP32, 5, 1),
	"remw": r(opOP32, 6, 1), "remuw": r(opOP32, 7, 1),
	// 即値演算 (I)
	"addi": i(opIMM, 0), "slti": i(opIMM, 2), "sltiu": i(opIMM, 3), "xori": i(opIMM, 4),
	"ori": i(opIMM, 6), "andi": i(opIMM, 7), "addiw": i(opIMM32, 0),
	"slli": sh(opIMM, 1, 0, fShift), "srli": sh(opIMM, 5, 0, fShift), "srai": sh(opIMM, 5, 0x20, fShift),
	"slliw": sh(opIMM32, 1, 0, fShiftW), "srliw": sh(opIMM32, 5, 0, fShiftW), "sraiw": sh(opIMM32, 5, 0x20, fShiftW),
	// ロード (I) / ストア (S)
	"lb": ld(0), "lh": ld(1), "lw": ld(2), "ld": ld(3), "lbu": ld(4), "lhu": ld(5), "lwu": ld(6),
	"sb": st(0), "sh": st(1), "sw": st(2), "sd": st(3),
	// 分岐 (B)
	"beq": br(0), "bne": br(1), "blt": br(4), "bge": br(5), "bltu": br(6), "bgeu": br(7),
	// U / J / jalr
	"lui":   {fU, opLUI, "d,i"},
	"auipc": {fU, opAUIPC, "d,i"},
	"jal":   {fJ, opJAL, "d,i"},
	"jalr":  {fI, opJALR, "d,m"},
	// システム
	"ecall":  {fSys, opSYSTEM, ""},
	"ebreak": {fSys, 1<<20 | opSYSTEM, ""},
	"fence":  {fSys, 0x0ff0000f, ""}, // fence iorw, iorw 固定
}

// inst は 1 つの機械命令 (擬似命令展開後) 。
type inst struct {
	op           string
	rd, rs1, rs2 int
	imm          expr
	rel          uint32 // 0 以外なら、このリロケーションを強制的に出す (call/la 用)
	label        string // この命令の位置に定義するローカルラベル (%pcrel_hi 用)
}

// encode は即値 imm を確定した命令をエンコードする。
func encode(in inst, imm int64) (uint32, error) {
	info, ok := opTable[in.op]
	if !ok {
		return 0, fmt.Errorf("unknown instruction %q", in.op)
	}
	bits, err := immBits(info.f, imm)
	if err != nil {
		return 0, fmt.Errorf("%s: %v", in.op, err)
	}
	return info.base | uint32(in.rd)<<7 | uint32(in.rs1)<<15 | uint32(in.rs2)<<20 | bits, nil
}

// immBits は即値を形式ごとのビット位置に並べ替える。
//
//	I: imm[11:0]                → [31:20]
//	S: imm[11:5]→[31:25]  imm[4:0]→[11:7]
//	B: imm[12]→31 imm[10:5]→[30:25] imm[4:1]→[11:8] imm[11]→7
//	U: imm[31:12]               → [31:12]   (ここでは imm は上位 20bit の値)
//	J: imm[20]→31 imm[10:1]→[30:21] imm[11]→20 imm[19:12]→[19:12]
func immBits(f format, imm int64) (uint32, error) {
	u := uint32(imm)
	switch f {
	case fR, fSys:
		return 0, nil
	case fI:
		if imm < -2048 || imm > 2047 {
			return 0, fmt.Errorf("immediate %d out of range [-2048, 2047]", imm)
		}
		return (u & 0xfff) << 20, nil
	case fShift, fShiftW:
		max := int64(63)
		if f == fShiftW {
			max = 31
		}
		if imm < 0 || imm > max {
			return 0, fmt.Errorf("shift amount %d out of range [0, %d]", imm, max)
		}
		return u << 20, nil
	case fS:
		if imm < -2048 || imm > 2047 {
			return 0, fmt.Errorf("offset %d out of range [-2048, 2047]", imm)
		}
		return (u>>5&0x7f)<<25 | (u&0x1f)<<7, nil
	case fB:
		if imm < -4096 || imm > 4094 || imm&1 != 0 {
			return 0, fmt.Errorf("branch offset %d out of range (±4KiB, even)", imm)
		}
		return (u>>12&1)<<31 | (u>>5&0x3f)<<25 | (u>>1&0xf)<<8 | (u>>11&1)<<7, nil
	case fU:
		if imm < -0x80000 || imm > 0xfffff {
			return 0, fmt.Errorf("upper immediate %#x out of range [0, 0xfffff]", imm)
		}
		return (u & 0xfffff) << 12, nil
	case fJ:
		if imm < -(1<<20) || imm >= 1<<20 || imm&1 != 0 {
			return 0, fmt.Errorf("jump offset %d out of range (±1MiB, even)", imm)
		}
		return (u>>20&1)<<31 | (u>>1&0x3ff)<<21 | (u>>11&1)<<20 | (u>>12&0xff)<<12, nil
	}
	return 0, fmt.Errorf("bad format")
}

// immMask は形式 f の即値が占めるビット (リロケーション適用時に一度クリアする)。
func immMask(f format) uint32 {
	switch f {
	case fI:
		return 0xfff00000
	case fS:
		return 0xfe000f80
	case fB:
		return 0xfe000f80
	case fU, fJ:
		return 0xfffff000
	}
	return 0
}

// patchImm は既存の命令語 w の即値部分を imm で置き換える (リンカ用)。
func patchImm(w uint32, f format, imm int64) (uint32, error) {
	bits, err := immBits(f, imm)
	if err != nil {
		return 0, err
	}
	return w&^immMask(f) | bits, nil
}

// hi20 / lo12 : 32bit 値を lui/auipc + addi に分割する。
// addi の即値は符号拡張されるので、lo が負になる場合に備えて +0x800 して丸める。
func hi20(v int64) int64 { return ((v + 0x800) >> 12) & 0xfffff }
func lo12(v int64) int64 { return signExtend(v, 12) }

func signExtend(v int64, bits uint) int64 {
	shift := 64 - bits
	return v << shift >> shift
}
