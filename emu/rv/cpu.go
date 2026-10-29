package rv

import (
	"fmt"
	"io"
	"math"
	"math/bits"
)

// CPU は RISC-V の 1 ハート (hart = ハードウェアスレッド) の状態。
type CPU struct {
	X      [32]uint64 // 汎用レジスタ x0..x31 (x0 は常に 0)
	PC     uint64
	Priv   uint64 // 現在の特権レベル (PrivU / PrivS / PrivM)
	csr    [4096]uint64
	nextPC uint64 // 実行中の命令が決める次の PC

	Bus   *Bus
	Clint *CLINT // nil ならタイマなし (ユーザモードランナー)

	// LR/SC 用の予約
	reserved    bool
	reservation uint64

	Steps uint64 // 実行した命令数 (= instret)

	// ユーザモードランナー用: ecall を直接ホスト側で処理する
	UserOnly bool
	Syscall  func(c *CPU)

	// WFI で待つときに呼ばれる (実時間で待ちたい場合など)
	IdleHook func(ticks uint64)
	// IdleLimit が nil でなければ、WFI で早送りする量 (want tick) を制限できる。
	// 戻り値だけ mtime を進める (0 なら進めない = WFI は何もせず次の命令へ)。
	IdleLimit func(want uint64) uint64

	// OnTrap はトラップ (例外・割り込み) と mret/sret のたびに呼ばれる (nil なら何もしない)。
	OnTrap func(ev *TrapEvent)
	// Ring が nil でなければ、実行した命令を直近 N 個だけ記録する。
	Ring *TraceRing

	Trace io.Writer // nil でなければ 1 命令ごとにトレースを出力

	Halted   bool
	ExitCode int
	Err      error
}

func NewCPU(bus *Bus) *CPU {
	c := &CPU{Bus: bus, Priv: PrivM}
	c.resetCSRs()
	return c
}

// Halt はエミュレーションを止める。
func (c *CPU) Halt(code int, err error) {
	c.Halted, c.ExitCode, c.Err = true, code, err
}

// Step は 1 命令 (または 1 回の割り込み受付) を実行する。
func (c *CPU) Step() {
	if c.Clint != nil {
		c.Clint.Tick()
		c.updateTimerBits()
		if c.checkInterrupt() {
			return
		}
	}

	in, exc := c.fetch()
	if exc == nil {
		if c.Ring != nil {
			c.Ring.add(c.Steps, c.PC, c.Priv, in)
		}
		if c.Trace != nil {
			fmt.Fprintf(c.Trace, "%10d %c %016x: %s\n", c.Steps, "USHM"[c.Priv], c.PC, Disasm(in, c.PC))
		}
		c.nextPC = c.PC + in.Size
		exc = c.execute(in)
	}
	if exc != nil {
		c.trap(exc.Cause, exc.Tval)
		return
	}
	c.X[0] = 0
	c.PC = c.nextPC
	c.Steps++
}

// updateTimerBits は CLINT の状態を mip の MTIP/MSIP ビットに反映する。
func (c *CPU) updateTimerBits() {
	mip := c.csr[csrMip] &^ (1<<irqMTimer | 1<<irqMSoft)
	if c.Clint.TimerPending() {
		mip |= 1 << irqMTimer
	}
	if c.Clint.Msip&1 != 0 {
		mip |= 1 << irqMSoft
	}
	c.csr[csrMip] = mip
}

// fetch は PC から命令を読み、デコードする (圧縮命令なら展開してから)。
func (c *CPU) fetch() (Inst, *Exception) {
	lo, ok := c.Bus.Load(c.PC, 2)
	if !ok {
		return Inst{}, &Exception{excInstAccessFault, c.PC}
	}
	if lo&3 != 3 { // 圧縮命令
		raw, ok := expandCompressed(uint32(lo))
		if !ok {
			return Inst{}, &Exception{excIllegalInst, lo}
		}
		in := Decode(raw)
		in.Size, in.Compressed = 2, uint32(lo)
		return in, nil
	}
	hi, ok := c.Bus.Load(c.PC+2, 2)
	if !ok {
		return Inst{}, &Exception{excInstAccessFault, c.PC}
	}
	return Decode(uint32(hi<<16 | lo)), nil
}

func (c *CPU) load(addr uint64, size int) (uint64, *Exception) {
	v, ok := c.Bus.Load(addr, size)
	if !ok {
		return 0, &Exception{excLoadAccessFault, addr}
	}
	return v, nil
}

func (c *CPU) store(addr uint64, size int, v uint64) *Exception {
	if c.reserved && c.reservation == addr {
		c.reserved = false // 他のストアは予約を壊す
	}
	if !c.Bus.Store(addr, size, v) {
		return &Exception{excStoreFault, addr}
	}
	return nil
}

var loadSize = [numOps]int{LB: 1, LBU: 1, LH: 2, LHU: 2, LW: 4, LWU: 4, LD: 8}

func illegal(in Inst) *Exception { return &Exception{excIllegalInst, uint64(in.Raw)} }

// 32 ビット演算の結果を 64 ビットに符号拡張する (RV64 の *W 命令)
func sx32(v uint64) uint64 { return uint64(int64(int32(v))) }

func b2u(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// execute は 1 命令の意味を実装する。
func (c *CPU) execute(in Inst) *Exception {
	x := &c.X
	rs1, rs2 := x[in.Rs1], x[in.Rs2]
	imm := uint64(in.Imm)
	rd := &x[in.Rd] // x0 への書き込みは Step の最後で 0 に戻す

	switch in.Op {
	case LUI:
		*rd = imm
	case AUIPC:
		*rd = c.PC + imm
	case JAL:
		*rd, c.nextPC = c.PC+in.Size, c.PC+imm
	case JALR:
		target := (rs1 + imm) &^ 1 // rd == rs1 の場合に備えて先に計算
		*rd, c.nextPC = c.PC+in.Size, target

	case BEQ, BNE, BLT, BGE, BLTU, BGEU:
		var taken bool
		switch in.Op {
		case BEQ:
			taken = rs1 == rs2
		case BNE:
			taken = rs1 != rs2
		case BLT:
			taken = int64(rs1) < int64(rs2)
		case BGE:
			taken = int64(rs1) >= int64(rs2)
		case BLTU:
			taken = rs1 < rs2
		case BGEU:
			taken = rs1 >= rs2
		}
		if taken {
			c.nextPC = c.PC + imm
		}

	case LB, LH, LW, LD, LBU, LHU, LWU:
		v, exc := c.load(rs1+imm, loadSize[in.Op])
		if exc != nil {
			return exc
		}
		switch in.Op { // 符号付きロードは符号拡張
		case LB:
			v = uint64(int8(v))
		case LH:
			v = uint64(int16(v))
		case LW:
			v = uint64(int32(v))
		}
		*rd = v
	case SB:
		return c.store(rs1+imm, 1, rs2)
	case SH:
		return c.store(rs1+imm, 2, rs2)
	case SW:
		return c.store(rs1+imm, 4, rs2)
	case SD:
		return c.store(rs1+imm, 8, rs2)

	// 即値演算 (I 形式)
	case ADDI:
		*rd = rs1 + imm
	case SLTI:
		*rd = b2u(int64(rs1) < in.Imm)
	case SLTIU:
		*rd = b2u(rs1 < imm)
	case XORI:
		*rd = rs1 ^ imm
	case ORI:
		*rd = rs1 | imm
	case ANDI:
		*rd = rs1 & imm
	case SLLI:
		*rd = rs1 << imm
	case SRLI:
		*rd = rs1 >> imm
	case SRAI:
		*rd = uint64(int64(rs1) >> imm)
	case ADDIW:
		*rd = sx32(rs1 + imm)
	case SLLIW:
		*rd = sx32(rs1 << imm)
	case SRLIW:
		*rd = sx32(uint64(uint32(rs1) >> imm))
	case SRAIW:
		*rd = sx32(uint64(int32(rs1) >> imm))

	// レジスタ演算 (R 形式)。シフト量は下位 6 ビット (W 命令は 5 ビット)
	case ADD:
		*rd = rs1 + rs2
	case SUB:
		*rd = rs1 - rs2
	case SLL:
		*rd = rs1 << (rs2 & 63)
	case SLT:
		*rd = b2u(int64(rs1) < int64(rs2))
	case SLTU:
		*rd = b2u(rs1 < rs2)
	case XOR:
		*rd = rs1 ^ rs2
	case SRL:
		*rd = rs1 >> (rs2 & 63)
	case SRA:
		*rd = uint64(int64(rs1) >> (rs2 & 63))
	case OR:
		*rd = rs1 | rs2
	case AND:
		*rd = rs1 & rs2
	case ADDW:
		*rd = sx32(rs1 + rs2)
	case SUBW:
		*rd = sx32(rs1 - rs2)
	case SLLW:
		*rd = sx32(rs1 << (rs2 & 31))
	case SRLW:
		*rd = sx32(uint64(uint32(rs1) >> (rs2 & 31)))
	case SRAW:
		*rd = sx32(uint64(int32(rs1) >> (rs2 & 31)))

	// M 拡張
	case MUL, MULH, MULHSU, MULHU, DIV, DIVU, REM, REMU, MULW, DIVW, DIVUW, REMW, REMUW:
		*rd = mulDiv(in.Op, rs1, rs2)

	// A 拡張
	case LR, SC, AMOSWAP, AMOADD, AMOXOR, AMOAND, AMOOR, AMOMIN, AMOMAX, AMOMINU, AMOMAXU:
		return c.atomic(in, rs1, rs2)

	case FENCE, FENCE_I, SFENCE_VMA:
		// 1 ハート・キャッシュなし・ページングなしなので何もしなくてよい

	case ECALL:
		if c.Syscall != nil {
			c.Syscall(c) // ユーザモードランナー: ホストで Linux システムコールを処理
			return nil
		}
		return &Exception{excEcallU + c.Priv, 0} // U=8, S=9, M=11
	case EBREAK:
		return &Exception{excBreakpoint, c.PC}
	case MRET:
		if c.Priv < PrivM {
			return illegal(in)
		}
		c.mret()
	case SRET:
		if c.Priv < PrivS {
			return illegal(in)
		}
		c.sret()
	case WFI:
		c.wfi()

	case CSRRW, CSRRS, CSRRC, CSRRWI, CSRRSI, CSRRCI:
		return c.csrOp(in, rs1)

	default:
		return illegal(in)
	}
	return nil
}

// mulDiv は M 拡張。0 除算やオーバーフローでも例外にならず、決められた値を返す。
func mulDiv(op Op, a, b uint64) uint64 {
	sa, sb := int64(a), int64(b)
	switch op {
	case MUL:
		return a * b
	case MULH: // 符号付き × 符号付きの上位 64 ビット
		hi, _ := bits.Mul64(a, b)
		if sa < 0 {
			hi -= b
		}
		if sb < 0 {
			hi -= a
		}
		return hi
	case MULHSU: // 符号付き × 符号なし
		hi, _ := bits.Mul64(a, b)
		if sa < 0 {
			hi -= b
		}
		return hi
	case MULHU:
		hi, _ := bits.Mul64(a, b)
		return hi
	case DIV:
		switch {
		case b == 0:
			return math.MaxUint64 // -1
		case sa == math.MinInt64 && sb == -1:
			return a // オーバーフロー
		}
		return uint64(sa / sb)
	case DIVU:
		if b == 0 {
			return math.MaxUint64
		}
		return a / b
	case REM:
		switch {
		case b == 0:
			return a
		case sa == math.MinInt64 && sb == -1:
			return 0
		}
		return uint64(sa % sb)
	case REMU:
		if b == 0 {
			return a
		}
		return a % b
	}

	// 32 ビット版 (結果は符号拡張)
	wa, wb := int32(a), int32(b)
	ua, ub := uint32(a), uint32(b)
	switch op {
	case MULW:
		return sx32(uint64(wa * wb))
	case DIVW:
		switch {
		case wb == 0:
			return math.MaxUint64
		case wa == math.MinInt32 && wb == -1:
			return sx32(uint64(uint32(wa)))
		}
		return sx32(uint64(wa / wb))
	case DIVUW:
		if ub == 0 {
			return math.MaxUint64
		}
		return sx32(uint64(ua / ub))
	case REMW:
		switch {
		case wb == 0:
			return sx32(uint64(ua))
		case wa == math.MinInt32 && wb == -1:
			return 0
		}
		return sx32(uint64(wa % wb))
	case REMUW:
		if ub == 0 {
			return sx32(uint64(ua))
		}
		return sx32(uint64(ua % ub))
	}
	panic("unreachable")
}

// atomic は A 拡張。ハートが 1 つなので「読んで、計算して、書く」を順にやれば原子的。
func (c *CPU) atomic(in Inst, addr, src uint64) *Exception {
	w := in.Width
	signExt := func(v uint64) uint64 { // .w は 32 ビット値を符号拡張して扱う
		if w == 4 {
			return sx32(v)
		}
		return v
	}
	if addr%uint64(w) != 0 {
		return &Exception{excStoreMisaligned, addr}
	}

	switch in.Op {
	case LR:
		v, exc := c.load(addr, w)
		if exc != nil {
			return exc
		}
		c.X[in.Rd] = signExt(v)
		c.reserved, c.reservation = true, addr
		return nil
	case SC:
		if !c.reserved || c.reservation != addr {
			c.X[in.Rd] = 1 // 失敗
			return nil
		}
		c.reserved = false
		if exc := c.store(addr, w, src); exc != nil {
			return exc
		}
		c.X[in.Rd] = 0 // 成功
		return nil
	}

	old, exc := c.load(addr, w)
	if exc != nil {
		return &Exception{excStoreFault, addr} // AMO の失敗はストア扱い
	}
	a, b := signExt(old), signExt(src)
	var v uint64
	switch in.Op {
	case AMOSWAP:
		v = b
	case AMOADD:
		v = a + b
	case AMOXOR:
		v = a ^ b
	case AMOAND:
		v = a & b
	case AMOOR:
		v = a | b
	case AMOMIN:
		v = a
		if int64(b) < int64(a) {
			v = b
		}
	case AMOMAX:
		v = a
		if int64(b) > int64(a) {
			v = b
		}
	case AMOMINU, AMOMAXU:
		ua, ub := a, b
		if w == 4 {
			ua, ub = uint64(uint32(a)), uint64(uint32(b))
		}
		v = a
		if (in.Op == AMOMINU && ub < ua) || (in.Op == AMOMAXU && ub > ua) {
			v = b
		}
	}
	if exc := c.store(addr, w, v); exc != nil {
		return exc
	}
	c.X[in.Rd] = a
	return nil
}

// csrOp は Zicsr の 6 命令。読み出し値を rd に、新しい値を CSR に書く。
func (c *CPU) csrOp(in Inst, rs1 uint64) *Exception {
	src := rs1
	if in.Op == CSRRWI || in.Op == CSRRSI || in.Op == CSRRCI {
		src = uint64(in.Imm)
	}
	// csrrw は常に書く。csrrs/csrrc は rs1 (uimm) が 0 なら書かない
	write := in.Op == CSRRW || in.Op == CSRRWI || in.Rs1 != 0
	if !c.csrAccessOK(in.Csr, write) {
		return illegal(in)
	}

	old := c.readCSR(in.Csr)
	if write {
		switch in.Op {
		case CSRRW, CSRRWI:
			c.writeCSR(in.Csr, src)
		case CSRRS, CSRRSI:
			c.writeCSR(in.Csr, old|src)
		case CSRRC, CSRRCI:
			c.writeCSR(in.Csr, old&^src)
		}
	}
	c.X[in.Rd] = old
	return nil
}

// wfi (Wait For Interrupt): 割り込みが来るまで待つ。
// エミュレータでは「次のタイマ割り込みの時刻まで mtime を早送り」する。
func (c *CPU) wfi() {
	if c.Clint == nil || c.csr[csrMip]&c.csr[csrMie] != 0 {
		return
	}
	if c.csr[csrMie]&(1<<irqMTimer) == 0 {
		// 起こしてくれる割り込みが無い = 永久に止まる (パニック後のループなど)
		c.Halt(0, fmt.Errorf("hart stopped: wfi with no interrupt enabled (pc=0x%x)", c.PC))
		return
	}
	if c.Clint.Mtimecmp > c.Clint.Mtime {
		delta := c.Clint.Mtimecmp - c.Clint.Mtime
		if c.IdleLimit != nil {
			delta = min(delta, c.IdleLimit(delta))
		}
		if c.IdleHook != nil {
			c.IdleHook(delta)
		}
		c.Clint.Mtime += delta
	}
}
