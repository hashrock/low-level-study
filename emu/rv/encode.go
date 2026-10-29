package rv

// 32 ビット命令のエンコーダ。
// 圧縮命令 (RVC) を通常命令へ展開するときと、テスト用のミニアセンブラで使う。
//
//  R 形式: | funct7 | rs2 | rs1 | funct3 | rd | opcode |
//  I 形式: |   imm[11:0]  | rs1 | funct3 | rd | opcode |
//  S 形式: | imm[11:5] | rs2 | rs1 | funct3 | imm[4:0] | opcode |
//  B 形式: S 形式と同じ位置に imm[12|10:5] と imm[4:1|11]
//  U 形式: |        imm[31:12]           | rd | opcode |
//  J 形式: | imm[20|10:1|11|19:12]       | rd | opcode |

// 主要な opcode (命令の下位 7 ビット)
const (
	opLoad    = 0x03
	opMiscMem = 0x0f
	opOpImm   = 0x13
	opAuipc   = 0x17
	opOpImm32 = 0x1b
	opStore   = 0x23
	opAmo     = 0x2f
	opOp      = 0x33
	opLui     = 0x37
	opOp32    = 0x3b
	opBranch  = 0x63
	opJalr    = 0x67
	opJal     = 0x6f
	opSystem  = 0x73
)

func encR(op, rd, f3, rs1, rs2, f7 uint32) uint32 {
	return f7<<25 | rs2<<20 | rs1<<15 | f3<<12 | rd<<7 | op
}

func encI(op, rd, f3, rs1 uint32, imm int64) uint32 {
	return uint32(imm&0xfff)<<20 | rs1<<15 | f3<<12 | rd<<7 | op
}

func encS(op, f3, rs1, rs2 uint32, imm int64) uint32 {
	i := uint32(imm)
	return (i>>5&0x7f)<<25 | rs2<<20 | rs1<<15 | f3<<12 | (i&0x1f)<<7 | op
}

func encB(f3, rs1, rs2 uint32, imm int64) uint32 {
	i := uint32(imm)
	return (i>>12&1)<<31 | (i>>5&0x3f)<<25 | rs2<<20 | rs1<<15 | f3<<12 |
		(i>>1&0xf)<<8 | (i>>11&1)<<7 | opBranch
}

func encU(op, rd uint32, imm int64) uint32 {
	return uint32(imm)&0xfffff000 | rd<<7 | op
}

func encJ(rd uint32, imm int64) uint32 {
	i := uint32(imm)
	return (i>>20&1)<<31 | (i>>1&0x3ff)<<21 | (i>>11&1)<<20 | (i>>12&0xff)<<12 | rd<<7 | opJal
}
