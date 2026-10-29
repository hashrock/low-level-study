package main

// pseudo.go — 1 つの文を機械命令 (inst) の列に展開する。
//
// 基本命令は opTable の shape に従ってオペランドを読むだけ。
// 擬似命令 (li, la, call, ...) はここで基本命令の並びに書き換える。
// 展開結果の命令数はパス 1 (サイズ計算) とパス 2 (エンコード) で一致する必要がある。

import (
	"fmt"
	"math/bits"
	"strings"
)

const (
	zero = 0
	ra   = 1
	t1   = 6
)

// expand は文を機械命令列に展開する。
// lookup は .equ 定数の解決、newLabel は la 用のローカルラベル生成。
func expand(st stmt, lookup resolver, newLabel func() string) ([]inst, error) {
	a := st.args
	reg := func(k int) (int, error) { return parseReg(a[k]) }
	ex := func(k int) (expr, error) { return parseExpr(a[k], lookup) }
	want := func(n int) error {
		if len(a) != n {
			return fmt.Errorf("%s: expected %d operands, got %d", st.name, n, len(a))
		}
		return nil
	}
	// 引数パターンごとの小さなヘルパ
	twoRegs := func() (int, int, error) { // rd, rs
		if err := want(2); err != nil {
			return 0, 0, err
		}
		d, err := reg(0)
		if err != nil {
			return 0, 0, err
		}
		s, err := reg(1)
		return d, s, err
	}
	regLabel := func() (int, expr, error) { // rs, label
		if err := want(2); err != nil {
			return 0, expr{}, err
		}
		s, err := reg(0)
		if err != nil {
			return 0, expr{}, err
		}
		e, err := ex(1)
		return s, e, err
	}
	one := func(op string, rd, rs1, rs2 int, imm expr) []inst {
		return []inst{{op: op, rd: rd, rs1: rs1, rs2: rs2, imm: imm}}
	}
	num := func(v int64) expr { return expr{add: v} }

	switch st.name {
	case "nop":
		return one("addi", zero, zero, 0, num(0)), want(0)
	case "ret":
		return one("jalr", zero, ra, 0, num(0)), want(0)

	case "mv", "not", "neg", "negw", "sext.w", "seqz", "snez", "sltz", "sgtz":
		d, s, err := twoRegs()
		m := map[string]inst{
			"mv":     {op: "addi", rd: d, rs1: s},
			"not":    {op: "xori", rd: d, rs1: s, imm: num(-1)},
			"neg":    {op: "sub", rd: d, rs1: zero, rs2: s},
			"negw":   {op: "subw", rd: d, rs1: zero, rs2: s},
			"sext.w": {op: "addiw", rd: d, rs1: s},
			"seqz":   {op: "sltiu", rd: d, rs1: s, imm: num(1)},
			"snez":   {op: "sltu", rd: d, rs1: zero, rs2: s},
			"sltz":   {op: "slt", rd: d, rs1: s, rs2: zero},
			"sgtz":   {op: "slt", rd: d, rs1: zero, rs2: s},
		}
		return []inst{m[st.name]}, err

	case "beqz", "bnez", "blez", "bgez", "bltz", "bgtz":
		s, e, err := regLabel()
		m := map[string]inst{
			"beqz": {op: "beq", rs1: s, rs2: zero},
			"bnez": {op: "bne", rs1: s, rs2: zero},
			"blez": {op: "bge", rs1: zero, rs2: s}, // 0 >= s
			"bgez": {op: "bge", rs1: s, rs2: zero},
			"bltz": {op: "blt", rs1: s, rs2: zero},
			"bgtz": {op: "blt", rs1: zero, rs2: s}, // 0 < s
		}
		in := m[st.name]
		in.imm = e
		return []inst{in}, err

	case "bgt", "ble", "bgtu", "bleu":
		// オペランドを入れ替えて blt/bge に: bgt a,b,L == blt b,a,L
		if err := want(3); err != nil {
			return nil, err
		}
		x, err1 := reg(0)
		y, err2 := reg(1)
		e, err3 := ex(2)
		op := map[string]string{"bgt": "blt", "ble": "bge", "bgtu": "bltu", "bleu": "bgeu"}[st.name]
		return one(op, 0, y, x, e), firstErr(err1, err2, err3)

	case "j":
		if err := want(1); err != nil {
			return nil, err
		}
		e, err := ex(0)
		return one("jal", zero, 0, 0, e), err
	case "jal":
		if len(a) == 1 { // jal label == jal ra, label
			e, err := ex(0)
			return one("jal", ra, 0, 0, e), err
		}
	case "jr":
		if err := want(1); err != nil {
			return nil, err
		}
		s, err := reg(0)
		return one("jalr", zero, s, 0, num(0)), err
	case "jalr":
		if len(a) == 1 { // jalr rs == jalr ra, 0(rs)
			s, err := reg(0)
			return one("jalr", ra, s, 0, num(0)), err
		}
		if len(a) == 3 { // jalr rd, rs, imm
			d, err1 := reg(0)
			s, err2 := reg(1)
			e, err3 := ex(2)
			return one("jalr", d, s, 0, e), firstErr(err1, err2, err3)
		}

	case "call", "tail":
		// auipc + jalr のペアに R_RISCV_CALL_PLT を 1 つ付ける。
		// リンカがオフセットを計算して両方の即値を埋める。
		if err := want(1); err != nil {
			return nil, err
		}
		e, err := ex(0)
		link, tmp := ra, ra
		if st.name == "tail" {
			link, tmp = zero, t1
		}
		return []inst{
			{op: "auipc", rd: tmp, imm: e, rel: rCallPLT},
			{op: "jalr", rd: link, rs1: tmp},
		}, err

	case "la", "lla":
		// auipc rd, %pcrel_hi(sym)  /  addi rd, rd, %pcrel_lo(.Lpcrel_hiN)
		// %pcrel_lo は「対応する auipc の位置」を指すラベルを参照するのが ELF の決まり。
		d, e, err := regLabel()
		if err != nil {
			return nil, err
		}
		if e.mod != "" {
			return nil, fmt.Errorf("la: unexpected modifier")
		}
		l := newLabel()
		return []inst{
			{op: "auipc", rd: d, imm: expr{sym: e.sym, add: e.add, mod: "pcrel_hi"}, label: l},
			{op: "addi", rd: d, rs1: d, imm: expr{sym: l, mod: "pcrel_lo"}},
		}, nil

	case "li":
		d, e, err := regLabel()
		if err != nil {
			return nil, err
		}
		if e.sym != "" || e.mod != "" {
			return nil, fmt.Errorf("li: operand must be a constant (use la for addresses; .equ must be defined before use)")
		}
		return liSeq(d, e.add), nil
	}

	// ---- 基本命令: shape に従ってオペランドを読む ----
	info, ok := opTable[st.name]
	if !ok {
		return nil, fmt.Errorf("unknown instruction %q", st.name)
	}
	in := inst{op: st.name}
	if st.name == "fence" {
		return []inst{in}, nil // fence のオペランドは無視 (常に iorw,iorw)
	}
	var fields []string
	if info.shape != "" {
		fields = strings.Split(info.shape, ",")
	}
	if err := want(len(fields)); err != nil {
		return nil, err
	}
	for k, f := range fields {
		var err error
		switch f {
		case "d":
			in.rd, err = reg(k)
		case "s":
			in.rs1, err = reg(k)
		case "t":
			in.rs2, err = reg(k)
		case "i":
			in.imm, err = ex(k)
		case "m":
			in.imm, in.rs1, err = parseMem(a[k], lookup)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %v", st.name, err)
		}
	}
	return []inst{in}, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// liSeq は任意の 64bit 定数を rd にロードする命令列を作る (LLVM と同じ考え方)。
//
//	32bit に収まる:  lui rd, hi20  +  addiw rd, rd, lo12   (片方だけのこともある)
//	それ以上     :  「下位 12bit を除いた上位部分」を再帰的に作り、
//	                 slli で左シフトしてから addi で下位 12bit を足す
func liSeq(rd int, v int64) []inst {
	if v == int64(int32(v)) {
		hi, lo := hi20(v), lo12(v)
		if hi == 0 {
			return []inst{{op: "addi", rd: rd, rs1: zero, imm: expr{add: lo}}}
		}
		seq := []inst{{op: "lui", rd: rd, imm: expr{add: hi}}}
		if lo != 0 {
			seq = append(seq, inst{op: "addiw", rd: rd, rs1: rd, imm: expr{add: lo}})
		}
		return seq
	}
	lo := lo12(v)
	hi52 := (uint64(v) + 0x800) >> 12 // 下位 12bit (符号付き) を引いた残り
	shift := 12 + bits.TrailingZeros64(hi52)
	hi := signExtend(int64(hi52>>(shift-12)), uint(64-shift))
	seq := liSeq(rd, hi)
	seq = append(seq, inst{op: "slli", rd: rd, rs1: rd, imm: expr{add: int64(shift)}})
	if lo != 0 {
		seq = append(seq, inst{op: "addi", rd: rd, rs1: rd, imm: expr{add: lo}})
	}
	return seq
}
