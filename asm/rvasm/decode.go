package rvasm

// decode.go — 32bit 命令語を形式 (R/I/S/B/U/J) とフィールドに分解する (可視化用)。
//
// エンコードの逆で、ビット列のどこが opcode / rd / funct3 / rs1 / rs2 / funct7 /
// 即値のどの部分なのかを MSB→LSB の順に並べて返す。

// Field は命令語の 1 つのビット範囲 [Hi:Lo] 。
type Field struct {
	Name  string `json:"name"`  // "opcode", "rd", "funct3", "rs1", "rs2", "funct7", "imm[11:0]" など
	Hi    int    `json:"hi"`    // 上端ビット (31..0)
	Lo    int    `json:"lo"`    // 下端ビット
	Value uint32 `json:"value"` // そのビット範囲の値 (右詰め)
}

// Encoding は命令語の分解結果。
type Encoding struct {
	Format string  `json:"format"` // "R" "I" "S" "B" "U" "J" (不明なら "?")
	Opcode uint32  `json:"opcode"`
	Rd     uint32  `json:"rd"` // 以下、その形式に無いフィールドは 0
	Funct3 uint32  `json:"funct3"`
	Rs1    uint32  `json:"rs1"`
	Rs2    uint32  `json:"rs2"`
	Funct7 uint32  `json:"funct7"`
	Imm    int64   `json:"imm"`    // 符号拡張した即値 (B/J はバイトオフセット、U は imm[31:12] の値 << 12)
	Fields []Field `json:"fields"` // 31→0 を隙間なく覆うビット範囲
}

func bitsOf(w uint32, hi, lo int) uint32 { return w >> uint(lo) & (1<<uint(hi-lo+1) - 1) }

// Decode は命令語 w を形式とフィールドに分解する。
func Decode(w uint32) Encoding {
	f := func(name string, hi, lo int) Field { return Field{name, hi, lo, bitsOf(w, hi, lo)} }
	e := Encoding{
		Opcode: bitsOf(w, 6, 0), Rd: bitsOf(w, 11, 7), Funct3: bitsOf(w, 14, 12),
		Rs1: bitsOf(w, 19, 15), Rs2: bitsOf(w, 24, 20), Funct7: bitsOf(w, 31, 25),
	}
	sw := int64(int32(w)) // 符号拡張用
	switch e.Opcode {
	case opOP, opOP32:
		e.Format = "R"
		e.Fields = []Field{f("funct7", 31, 25), f("rs2", 24, 20), f("rs1", 19, 15), f("funct3", 14, 12), f("rd", 11, 7), f("opcode", 6, 0)}
	case opIMM, opIMM32, 0x03, opJALR, opSYSTEM, 0x0f:
		e.Format = "I"
		e.Rs2, e.Funct7 = 0, 0
		e.Imm = sw >> 20
		switch {
		case e.Opcode == opIMM && (e.Funct3 == 1 || e.Funct3 == 5): // slli/srli/srai: shamt 6bit
			e.Funct7 = bitsOf(w, 31, 26) << 1
			e.Imm = int64(bitsOf(w, 25, 20))
			e.Fields = []Field{f("funct6", 31, 26), f("shamt", 25, 20)}
		case e.Opcode == opIMM32 && (e.Funct3 == 1 || e.Funct3 == 5): // slliw 等: shamt 5bit
			e.Funct7 = bitsOf(w, 31, 25)
			e.Imm = int64(bitsOf(w, 24, 20))
			e.Fields = []Field{f("funct7", 31, 25), f("shamt", 24, 20)}
		default:
			e.Fields = []Field{f("imm[11:0]", 31, 20)}
		}
		e.Fields = append(e.Fields, f("rs1", 19, 15), f("funct3", 14, 12), f("rd", 11, 7), f("opcode", 6, 0))
	case 0x23:
		e.Format = "S"
		e.Rd, e.Funct7 = 0, 0
		e.Imm = sw>>25<<5 | int64(bitsOf(w, 11, 7))
		e.Fields = []Field{f("imm[11:5]", 31, 25), f("rs2", 24, 20), f("rs1", 19, 15), f("funct3", 14, 12), f("imm[4:0]", 11, 7), f("opcode", 6, 0)}
	case 0x63:
		e.Format = "B"
		e.Rd, e.Funct7 = 0, 0
		e.Imm = sw>>31<<12 | int64(bitsOf(w, 7, 7))<<11 | int64(bitsOf(w, 30, 25))<<5 | int64(bitsOf(w, 11, 8))<<1
		e.Fields = []Field{f("imm[12]", 31, 31), f("imm[10:5]", 30, 25), f("rs2", 24, 20), f("rs1", 19, 15), f("funct3", 14, 12), f("imm[4:1]", 11, 8), f("imm[11]", 7, 7), f("opcode", 6, 0)}
	case opLUI, opAUIPC:
		e.Format = "U"
		e.Funct3, e.Rs1, e.Rs2, e.Funct7 = 0, 0, 0, 0
		e.Imm = sw >> 12 << 12
		e.Fields = []Field{f("imm[31:12]", 31, 12), f("rd", 11, 7), f("opcode", 6, 0)}
	case opJAL:
		e.Format = "J"
		e.Funct3, e.Rs1, e.Rs2, e.Funct7 = 0, 0, 0, 0
		e.Imm = sw>>31<<20 | int64(bitsOf(w, 19, 12))<<12 | int64(bitsOf(w, 20, 20))<<11 | int64(bitsOf(w, 30, 21))<<1
		e.Fields = []Field{f("imm[20]", 31, 31), f("imm[10:1]", 30, 21), f("imm[11]", 20, 20), f("imm[19:12]", 19, 12), f("rd", 11, 7), f("opcode", 6, 0)}
	default:
		e.Format = "?"
		e.Fields = []Field{f("?", 31, 7), f("opcode", 6, 0)}
	}
	return e
}
