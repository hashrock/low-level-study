package main

import "fmt"

// 逆アセンブラ (--trace 用)。Inst を人間が読める 1 行にする。

var sprintf = fmt.Sprintf

var regNames = [32]string{
	"zero", "ra", "sp", "gp", "tp", "t0", "t1", "t2", "s0", "s1",
	"a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7",
	"s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9", "s10", "s11",
	"t3", "t4", "t5", "t6",
}

// Disasm は "命令語  ニーモニック オペランド" の形式で返す。pc は分岐先の計算用。
func Disasm(in Inst, pc uint64) string {
	raw := sprintf("%08x", in.Raw)
	if in.Size == 2 {
		raw = sprintf("    %04x", in.Compressed)
	}
	return raw + "  " + disasmBody(in, pc)
}

func disasmBody(in Inst, pc uint64) string {
	rd, rs1, rs2 := regNames[in.Rd], regNames[in.Rs1], regNames[in.Rs2]
	name := in.Op.String()
	switch in.Op {
	case ILLEGAL:
		return sprintf("illegal 0x%08x", in.Raw)
	case LUI, AUIPC:
		return sprintf("%s %s, 0x%x", name, rd, uint64(in.Imm)>>12&0xfffff)
	case JAL:
		return sprintf("%s %s, 0x%x", name, rd, pc+uint64(in.Imm))
	case JALR:
		return sprintf("%s %s, %d(%s)", name, rd, in.Imm, rs1)
	case BEQ, BNE, BLT, BGE, BLTU, BGEU:
		return sprintf("%s %s, %s, 0x%x", name, rs1, rs2, pc+uint64(in.Imm))
	case LB, LH, LW, LD, LBU, LHU, LWU:
		return sprintf("%s %s, %d(%s)", name, rd, in.Imm, rs1)
	case SB, SH, SW, SD:
		return sprintf("%s %s, %d(%s)", name, rs2, in.Imm, rs1)
	case ADDI, SLTI, SLTIU, XORI, ORI, ANDI, SLLI, SRLI, SRAI, ADDIW, SLLIW, SRLIW, SRAIW:
		return sprintf("%s %s, %s, %d", name, rd, rs1, in.Imm)
	case LR:
		return sprintf("%s.%s %s, (%s)", name, widthSuffix(in), rd, rs1)
	case SC, AMOSWAP, AMOADD, AMOXOR, AMOAND, AMOOR, AMOMIN, AMOMAX, AMOMINU, AMOMAXU:
		return sprintf("%s.%s %s, %s, (%s)", name, widthSuffix(in), rd, rs2, rs1)
	case CSRRW, CSRRS, CSRRC:
		return sprintf("%s %s, %s, %s", name, rd, csrName(in.Csr), rs1)
	case CSRRWI, CSRRSI, CSRRCI:
		return sprintf("%s %s, %s, %d", name, rd, csrName(in.Csr), in.Imm)
	case FENCE, FENCE_I, ECALL, EBREAK, MRET, SRET, WFI, SFENCE_VMA:
		return name
	}
	return sprintf("%s %s, %s, %s", name, rd, rs1, rs2) // R 形式
}

func widthSuffix(in Inst) string {
	if in.Width == 8 {
		return "d"
	}
	return "w"
}
