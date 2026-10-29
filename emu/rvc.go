package main

// C 拡張 (圧縮命令) のデコード。
// 16 ビットの圧縮命令は、すべて「対応する 32 ビット命令」の短縮形なので、
// ここで 32 ビット命令に展開してから通常のデコーダに渡す。
// 下位 2 ビット (quadrant) が 0b11 以外なら圧縮命令。

// field は x の [hi:lo] ビットを取り出す。
func field(x uint32, hi, lo uint) uint32 { return x >> lo & (1<<(hi-lo+1) - 1) }

// expandCompressed は圧縮命令 c を 32 ビット命令に展開する。不正なら ok=false。
func expandCompressed(c uint32) (raw uint32, ok bool) {
	f3 := field(c, 15, 13)
	rd := field(c, 11, 7)     // フルのレジスタ番号
	rs2 := field(c, 6, 2)     //
	rdP := field(c, 4, 2) + 8 // 3 ビットの rd'/rs2' は x8..x15 を指す
	rs1P := field(c, 9, 7) + 8
	imm6 := sext(field(c, 12, 12)<<5|field(c, 6, 2), 6) // CI 形式の 6 ビット符号付き即値
	shamt := int64(field(c, 12, 12)<<5 | field(c, 6, 2))

	// LW/SW と LD/SD のオフセット (CL/CS 形式)
	offW := int64(field(c, 12, 10)<<3 | field(c, 6, 6)<<2 | field(c, 5, 5)<<6)
	offD := int64(field(c, 12, 10)<<3 | field(c, 6, 5)<<6)

	switch c&3<<3 | f3 {
	// ---- Quadrant 0 ----
	case 0<<3 | 0: // c.addi4spn rd', sp, nzuimm
		imm := field(c, 12, 11)<<4 | field(c, 10, 7)<<6 | field(c, 6, 6)<<2 | field(c, 5, 5)<<3
		if imm == 0 {
			return 0, false // 全ビット 0 の命令はここで不正扱いになる
		}
		return encI(opOpImm, rdP, 0, 2, int64(imm)), true
	case 0<<3 | 2: // c.lw
		return encI(opLoad, rdP, 2, rs1P, offW), true
	case 0<<3 | 3: // c.ld
		return encI(opLoad, rdP, 3, rs1P, offD), true
	case 0<<3 | 6: // c.sw
		return encS(opStore, 2, rs1P, rdP, offW), true
	case 0<<3 | 7: // c.sd
		return encS(opStore, 3, rs1P, rdP, offD), true

	// ---- Quadrant 1 ----
	case 1<<3 | 0: // c.addi (c.nop)
		return encI(opOpImm, rd, 0, rd, imm6), true
	case 1<<3 | 1: // c.addiw
		if rd == 0 {
			return 0, false
		}
		return encI(opOpImm32, rd, 0, rd, imm6), true
	case 1<<3 | 2: // c.li
		return encI(opOpImm, rd, 0, 0, imm6), true
	case 1<<3 | 3:
		if rd == 2 { // c.addi16sp
			imm := sext(field(c, 12, 12)<<9|field(c, 6, 6)<<4|field(c, 5, 5)<<6|field(c, 4, 3)<<7|field(c, 2, 2)<<5, 10)
			if imm == 0 {
				return 0, false
			}
			return encI(opOpImm, 2, 0, 2, imm), true
		}
		imm := sext(field(c, 12, 12)<<17|field(c, 6, 2)<<12, 18) // c.lui
		if imm == 0 {
			return 0, false
		}
		return encU(opLui, rd, imm), true
	case 1<<3 | 4: // 算術演算 (rd' = rs1')
		switch field(c, 11, 10) {
		case 0: // c.srli
			return encI(opOpImm, rs1P, 5, rs1P, shamt), true
		case 1: // c.srai (imm[10]=1 が算術シフトの印)
			return encI(opOpImm, rs1P, 5, rs1P, shamt|0x400), true
		case 2: // c.andi
			return encI(opOpImm, rs1P, 7, rs1P, imm6), true
		}
		rs2P := rdP
		switch field(c, 12, 12)<<2 | field(c, 6, 5) {
		case 0:
			return encR(opOp, rs1P, 0, rs1P, rs2P, 0x20), true // c.sub
		case 1:
			return encR(opOp, rs1P, 4, rs1P, rs2P, 0), true // c.xor
		case 2:
			return encR(opOp, rs1P, 6, rs1P, rs2P, 0), true // c.or
		case 3:
			return encR(opOp, rs1P, 7, rs1P, rs2P, 0), true // c.and
		case 4:
			return encR(opOp32, rs1P, 0, rs1P, rs2P, 0x20), true // c.subw
		case 5:
			return encR(opOp32, rs1P, 0, rs1P, rs2P, 0), true // c.addw
		}
	case 1<<3 | 5: // c.j
		imm := field(c, 12, 12)<<11 | field(c, 11, 11)<<4 | field(c, 10, 9)<<8 | field(c, 8, 8)<<10 |
			field(c, 7, 7)<<6 | field(c, 6, 6)<<7 | field(c, 5, 3)<<1 | field(c, 2, 2)<<5
		return encJ(0, sext(imm, 12)), true
	case 1<<3 | 6, 1<<3 | 7: // c.beqz / c.bnez
		imm := field(c, 12, 12)<<8 | field(c, 11, 10)<<3 | field(c, 6, 5)<<6 | field(c, 4, 3)<<1 | field(c, 2, 2)<<5
		return encB(f3-6, rs1P, 0, sext(imm, 9)), true

	// ---- Quadrant 2 ----
	case 2<<3 | 0: // c.slli
		return encI(opOpImm, rd, 1, rd, shamt), true
	case 2<<3 | 2: // c.lwsp
		if rd == 0 {
			return 0, false
		}
		off := field(c, 12, 12)<<5 | field(c, 6, 4)<<2 | field(c, 3, 2)<<6
		return encI(opLoad, rd, 2, 2, int64(off)), true
	case 2<<3 | 3: // c.ldsp
		if rd == 0 {
			return 0, false
		}
		off := field(c, 12, 12)<<5 | field(c, 6, 5)<<3 | field(c, 4, 2)<<6
		return encI(opLoad, rd, 3, 2, int64(off)), true
	case 2<<3 | 4:
		bit12 := field(c, 12, 12)
		switch {
		case bit12 == 0 && rs2 == 0: // c.jr
			if rd == 0 {
				return 0, false
			}
			return encI(opJalr, 0, 0, rd, 0), true
		case bit12 == 0: // c.mv
			return encR(opOp, rd, 0, 0, rs2, 0), true
		case rd == 0 && rs2 == 0: // c.ebreak
			return 0x00100073, true
		case rs2 == 0: // c.jalr
			return encI(opJalr, 1, 0, rd, 0), true
		default: // c.add
			return encR(opOp, rd, 0, rd, rs2, 0), true
		}
	case 2<<3 | 6: // c.swsp
		off := field(c, 12, 9)<<2 | field(c, 8, 7)<<6
		return encS(opStore, 2, 2, rs2, int64(off)), true
	case 2<<3 | 7: // c.sdsp
		off := field(c, 12, 10)<<3 | field(c, 9, 7)<<6
		return encS(opStore, 3, 2, rs2, int64(off)), true
	}
	// 浮動小数点のロード/ストア (c.fld 等) は未対応
	return 0, false
}
