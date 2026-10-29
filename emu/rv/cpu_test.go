package rv

import (
	"math"
	"testing"
)

// riscv-tests 風の命令単位テスト。
// 1 命令だけを RAM に置き、x1/x2 に入力を入れて 1 ステップ実行し、x3 を確認する。

const testBase = 0x8000_0000

func newTestCPU(prog ...uint32) *CPU {
	bus := NewBus(testBase, 1<<20)
	for i, w := range prog {
		bus.Store(testBase+uint64(4*i), 4, uint64(w))
	}
	c := NewCPU(bus)
	c.PC = testBase
	return c
}

func neg(v int64) uint64 { return uint64(v) }

// R 形式命令: rd=x3, rs1=x1, rs2=x2
func rr(op, f3, f7 uint32) uint32 { return encR(op, 3, f3, 1, 2, f7) }

// I 形式命令: rd=x3, rs1=x1
func ri(op, f3 uint32, imm int64) uint32 { return encI(op, 3, f3, 1, imm) }

func TestALU(t *testing.T) {
	tests := []struct {
		name     string
		inst     uint32
		a, b     uint64
		expected uint64
	}{
		{"add", rr(opOp, 0, 0), 3, 4, 7},
		{"add overflow", rr(opOp, 0, 0), math.MaxUint64, 2, 1},
		{"sub", rr(opOp, 0, 0x20), 3, 4, neg(-1)},
		{"sll", rr(opOp, 1, 0), 1, 63, 1 << 63},
		{"sll masks shamt", rr(opOp, 1, 0), 1, 64 + 3, 8},
		{"slt", rr(opOp, 2, 0), neg(-1), 1, 1},
		{"sltu", rr(opOp, 3, 0), neg(-1), 1, 0},
		{"xor", rr(opOp, 4, 0), 0xff00, 0x0ff0, 0xf0f0},
		{"srl", rr(opOp, 5, 0), 1 << 63, 63, 1},
		{"sra", rr(opOp, 5, 0x20), 1 << 63, 63, neg(-1)},
		{"or", rr(opOp, 6, 0), 0xf0, 0x0f, 0xff},
		{"and", rr(opOp, 7, 0), 0xf0, 0x3c, 0x30},
		{"addw sign-extends", rr(opOp32, 0, 0), 0x7fffffff, 1, neg(math.MinInt32)},
		{"subw", rr(opOp32, 0, 0x20), 0, 1, neg(-1)},
		{"sllw", rr(opOp32, 1, 0), 1, 31, neg(math.MinInt32)},
		{"srlw", rr(opOp32, 5, 0), 0xffffffff80000000, 31, 1},
		{"sraw", rr(opOp32, 5, 0x20), 0x80000000, 31, neg(-1)},

		{"addi", ri(opOpImm, 0, -5), 3, 0, neg(-2)},
		{"slti", ri(opOpImm, 2, 0), neg(-1), 0, 1},
		{"sltiu -1 = max", ri(opOpImm, 3, -1), 5, 0, 1},
		{"xori -1 = not", ri(opOpImm, 4, -1), 0, 0, neg(-1)},
		{"ori", ri(opOpImm, 6, 0x10), 1, 0, 0x11},
		{"andi", ri(opOpImm, 7, 0xff), 0x1234, 0, 0x34},
		{"slli 63", ri(opOpImm, 1, 63), 1, 0, 1 << 63},
		{"srli", ri(opOpImm, 5, 4), 0x100, 0, 0x10},
		{"srai", ri(opOpImm, 5, 0x400|4), neg(-256), 0, neg(-16)},
		{"addiw", ri(opOpImm32, 0, 1), 0x7fffffff, 0, neg(math.MinInt32)},
		{"slliw", ri(opOpImm32, 1, 4), 0x0fffffff, 0, neg(-16)},
		{"srliw", ri(opOpImm32, 5, 1), neg(-1), 0, 0x7fffffff},
		{"sraiw", ri(opOpImm32, 5, 0x400|1), 0x80000000, 0, neg(-1 << 30)},

		// M 拡張
		{"mul", rr(opOp, 0, 1), 6, neg(-7), neg(-42)},
		{"mulh", rr(opOp, 1, 1), neg(-1), neg(-1), 0},
		{"mulh neg", rr(opOp, 1, 1), neg(-2), 1 << 62, neg(-1)},
		{"mulhsu", rr(opOp, 2, 1), neg(-1), math.MaxUint64, neg(-1)},
		{"mulhu", rr(opOp, 3, 1), math.MaxUint64, math.MaxUint64, math.MaxUint64 - 1},
		{"div", rr(opOp, 4, 1), neg(-7), 2, neg(-3)},
		{"div by zero", rr(opOp, 4, 1), 7, 0, neg(-1)},
		{"div overflow", rr(opOp, 4, 1), 1 << 63, neg(-1), 1 << 63},
		{"divu", rr(opOp, 5, 1), neg(-1), 2, math.MaxUint64 / 2},
		{"divu by zero", rr(opOp, 5, 1), 7, 0, math.MaxUint64},
		{"rem", rr(opOp, 6, 1), neg(-7), 2, neg(-1)},
		{"rem by zero", rr(opOp, 6, 1), 7, 0, 7},
		{"rem overflow", rr(opOp, 6, 1), 1 << 63, neg(-1), 0},
		{"remu", rr(opOp, 7, 1), 7, 4, 3},
		{"mulw", rr(opOp32, 0, 1), 0x10000, 0x10000, 0},
		{"divw", rr(opOp32, 4, 1), neg(-8), 2, neg(-4)},
		{"divw overflow", rr(opOp32, 4, 1), 0x80000000, neg(-1), neg(math.MinInt32)},
		{"divuw", rr(opOp32, 5, 1), 0xffffffff, 2, 0x7fffffff},
		{"divuw by zero", rr(opOp32, 5, 1), 5, 0, neg(-1)},
		{"remw", rr(opOp32, 6, 1), neg(-7), 2, neg(-1)},
		{"remuw", rr(opOp32, 7, 1), 0xffffffff, 10, 5},
	}
	for _, tt := range tests {
		c := newTestCPU(tt.inst)
		c.X[1], c.X[2] = tt.a, tt.b
		c.Step()
		if c.X[3] != tt.expected {
			t.Errorf("%s (%s): got 0x%x, want 0x%x", tt.name, Disasm(Decode(tt.inst), 0), c.X[3], tt.expected)
		}
	}
}

func TestLuiAuipcJumps(t *testing.T) {
	c := newTestCPU(lui(3, 0x12345000))
	c.Step()
	if c.X[3] != 0x12345000 {
		t.Errorf("lui: 0x%x", c.X[3])
	}
	c = newTestCPU(encU(opAuipc, 3, -4096))
	c.Step()
	if c.X[3] != testBase-4096 {
		t.Errorf("auipc: 0x%x", c.X[3])
	}
	c = newTestCPU(encJ(1, 16))
	c.Step()
	if c.PC != testBase+16 || c.X[1] != testBase+4 {
		t.Errorf("jal: pc=0x%x ra=0x%x", c.PC, c.X[1])
	}
	// jalr rd==rs1: ターゲットは古い rs1 から計算される
	c = newTestCPU(encI(opJalr, 1, 0, 1, 3))
	c.X[1] = testBase + 0x100
	c.Step()
	if c.PC != testBase+0x102 || c.X[1] != testBase+4 {
		t.Errorf("jalr: pc=0x%x ra=0x%x", c.PC, c.X[1])
	}
}

func TestBranches(t *testing.T) {
	tests := []struct {
		f3    uint32
		a, b  uint64
		taken bool
	}{
		{0, 1, 1, true}, {0, 1, 2, false}, // beq
		{1, 1, 2, true}, {1, 2, 2, false}, // bne
		{4, neg(-1), 0, true}, {4, 0, neg(-1), false}, // blt
		{5, 0, 0, true}, {5, neg(-1), 0, false}, // bge
		{6, 0, neg(-1), true}, {6, neg(-1), 0, false}, // bltu
		{7, neg(-1), 0, true}, {7, 0, 1, false}, // bgeu
	}
	for _, tt := range tests {
		for _, off := range []int64{-8, 64} {
			c := newTestCPU(encB(tt.f3, 1, 2, off))
			c.X[1], c.X[2] = tt.a, tt.b
			c.Step()
			want := uint64(testBase + 4)
			if tt.taken {
				want = uint64(int64(testBase) + off)
			}
			if c.PC != want {
				t.Errorf("%s 0x%x,0x%x off=%d: pc=0x%x want 0x%x", Decode(encB(tt.f3, 1, 2, off)).Op, tt.a, tt.b, off, c.PC, want)
			}
		}
	}
}

func TestLoadStore(t *testing.T) {
	const addr = testBase + 0x1000
	// sd → 各幅のロードで符号拡張/ゼロ拡張を確認
	c := newTestCPU(encS(opStore, 3, 1, 2, 8))
	c.X[1], c.X[2] = addr-8, 0x80706050_f0e0d0c0
	c.Step()
	loads := []struct {
		f3   uint32
		want uint64
	}{
		{0, neg(-64)},            // lb  0xc0
		{4, 0xc0},                // lbu
		{1, neg(-0x2f40)},        // lh  0xd0c0
		{5, 0xd0c0},              // lhu
		{2, 0xfffffffff0e0d0c0},  // lw
		{6, 0xf0e0d0c0},          // lwu
		{3, 0x80706050_f0e0d0c0}, // ld
	}
	for _, l := range loads {
		in := encI(opLoad, 3, l.f3, 1, 0)
		c.Bus.Store(testBase, 4, uint64(in))
		c.PC, c.X[1] = testBase, addr
		c.Step()
		if c.X[3] != l.want {
			t.Errorf("%s: got 0x%x want 0x%x", Decode(in).Op, c.X[3], l.want)
		}
	}
	// sb/sh/sw は下位バイトだけ書く
	for f3, want := range []uint64{0x80706050_f0e0d011, 0x80706050_f0e02211, 0x80706050_44332211} {
		c.Bus.Store(testBase, 4, uint64(encS(opStore, uint32(f3), 1, 2, 0)))
		c.PC, c.X[1], c.X[2] = testBase, addr, 0x44332211
		c.Step()
		if v, _ := c.Bus.Load(addr, 8); v != want {
			t.Errorf("store f3=%d: memory 0x%x want 0x%x", f3, v, want)
		}
	}
}

func TestX0IsZero(t *testing.T) {
	c := newTestCPU(addi(0, 0, 5))
	c.Step()
	if c.X[0] != 0 {
		t.Fatal("x0 was written")
	}
}

func amo(f5, f3 uint32) uint32 { return encR(opAmo, 3, f3, 1, 2, f5<<2) }

func TestAtomics(t *testing.T) {
	const addr = testBase + 0x2000
	tests := []struct {
		name     string
		inst     uint32
		mem, src uint64
		wantMem  uint64
		wantRd   uint64
	}{
		{"amoswap.d", amo(0x01, 3), 5, 9, 9, 5},
		{"amoadd.d", amo(0x00, 3), 5, 9, 14, 5},
		{"amoadd.w wraps", amo(0x00, 2), 0x7fffffff, 1, 0x80000000, 0x7fffffff},
		{"amoxor.d", amo(0x04, 3), 0b1100, 0b1010, 0b0110, 0b1100},
		{"amoand.d", amo(0x0c, 3), 0b1100, 0b1010, 0b1000, 0b1100},
		{"amoor.d", amo(0x08, 3), 0b1100, 0b1010, 0b1110, 0b1100},
		{"amomin.d", amo(0x10, 3), 3, neg(-1), neg(-1), 3},
		{"amomax.d", amo(0x14, 3), 3, neg(-1), 3, 3},
		{"amominu.d", amo(0x18, 3), 3, neg(-1), 3, 3},
		{"amomaxu.d", amo(0x1c, 3), 3, neg(-1), neg(-1), 3},
		{"amomin.w", amo(0x10, 2), 0x80000000, 1, 0x80000000, neg(math.MinInt32)},
		{"amomaxu.w", amo(0x1c, 2), 0x80000000, 1, 0x80000000, neg(math.MinInt32)},
	}
	for _, tt := range tests {
		c := newTestCPU(tt.inst)
		c.Bus.Store(addr, 8, tt.mem)
		c.X[1], c.X[2] = addr, tt.src
		c.Step()
		size := 8
		if Decode(tt.inst).Width == 4 {
			size = 4
		}
		mem, _ := c.Bus.Load(addr, size)
		if mem != tt.wantMem || c.X[3] != tt.wantRd {
			t.Errorf("%s: mem=0x%x rd=0x%x, want mem=0x%x rd=0x%x", tt.name, mem, c.X[3], tt.wantMem, tt.wantRd)
		}
	}

	// LR/SC: 予約があれば成功 (rd=0)、2 回目は予約が消えているので失敗 (rd=1)
	lr := encR(opAmo, 4, 3, 1, 0, 0x02<<2)
	sc := encR(opAmo, 3, 3, 1, 2, 0x03<<2)
	c := newTestCPU(lr, sc, sc)
	c.Bus.Store(addr, 8, 42)
	c.X[1], c.X[2] = addr, 99
	c.Step()
	c.Step()
	if c.X[4] != 42 || c.X[3] != 0 {
		t.Errorf("lr/sc: loaded %d, sc result %d", c.X[4], c.X[3])
	}
	c.Step()
	if v, _ := c.Bus.Load(addr, 8); c.X[3] != 1 || v != 99 {
		t.Errorf("second sc should fail: rd=%d mem=%d", c.X[3], v)
	}
}

func TestCSR(t *testing.T) {
	c := newTestCPU(
		encI(opSystem, 0, 5, 0x1f, csrMscratch), // csrrwi zero, mscratch, 31
		encI(opSystem, 3, 6, 0x0, csrMscratch),  // csrrsi x3, mscratch, 0 (読むだけ)
		encI(opSystem, 4, 7, 0x3, csrMscratch),  // csrrci x4, mscratch, 3
		csrr(5, csrMscratch),                    // csrr x5, mscratch
		csrr(6, csrMisa),                        // misa
	)
	for i := 0; i < 5; i++ {
		c.Step()
	}
	if c.X[3] != 31 || c.X[4] != 31 || c.X[5] != 28 {
		t.Errorf("csr ops: x3=%d x4=%d x5=%d", c.X[3], c.X[4], c.X[5])
	}
	if c.X[6]>>62 != 2 || c.X[6]&(1<<('C'-'A')) == 0 {
		t.Errorf("misa = 0x%x", c.X[6])
	}

	// U モードから M モード CSR を触ると不正命令例外
	c = newTestCPU(csrr(3, csrMstatus))
	c.Priv = PrivU
	c.Step()
	if c.Priv != PrivM || c.csr[csrMcause] != excIllegalInst {
		t.Errorf("U-mode csr access: priv=%d mcause=%d", c.Priv, c.csr[csrMcause])
	}
}

// ecall で M モードへトラップし、mret で U モードの次の命令へ戻る。
func TestTrapAndMret(t *testing.T) {
	a := NewAsm(testBase)
	a.La(t0, "handler")
	a.Emit(csrw(csrMtvec, t0))
	a.La(t0, "user")
	a.Emit(csrw(csrMepc, t0), mret) // MPP=0 (U) なので U モードへ
	a.Label("user")
	a.Emit(addi(a0, zero, 7), ecall) // U モードで ecall
	a.Emit(addi(a1, zero, 1))        // 戻ってきたらここ
	a.Label("done")
	a.Jal(zero, "done")
	a.Label("handler")
	a.Emit(csrr(t1, csrMepc), addi(t1, t1, 4), csrw(csrMepc, t1), // ecall の次へ
		addi(a0, a0, 1), mret)

	c := NewCPU(NewBus(testBase, 1<<16))
	c.Bus.WriteBytes(testBase, a.Bytes())
	c.PC = testBase
	for i := 0; i < 20; i++ {
		c.Step()
	}
	if c.X[a0] != 8 || c.X[a1] != 1 || c.Priv != PrivU {
		t.Errorf("a0=%d a1=%d priv=%d", c.X[a0], c.X[a1], c.Priv)
	}
	if c.csr[csrMcause] != excEcallU {
		t.Errorf("mcause=%d", c.csr[csrMcause])
	}
}

// CLINT のタイマ割り込みが mtvec に飛び、mcause の最上位ビットが立つ。
func TestTimerInterrupt(t *testing.T) {
	a := NewAsm(testBase)
	a.La(t0, "handler")
	a.Emit(csrw(csrMtvec, t0))
	a.Li(t0, clintBase+0x4000)
	a.Emit(addi(t1, zero, 100), sd(t1, t0, 0))             // mtimecmp = 100
	a.Emit(addi(t1, zero, 1<<7), csrw(csrMie, t1))         // MTIE
	a.Emit(addi(t1, zero, 1<<3), csrrs(0, csrMstatus, t1)) // MIE
	a.Label("loop")
	a.Emit(wfi)
	a.Jal(zero, "loop")
	a.Label("handler")
	a.Emit(csrr(a0, csrMcause))
	a.Label("hang")
	a.Jal(zero, "hang")

	c := NewCPU(NewBus(testBase, 1<<16))
	c.Clint = NewCLINT()
	c.Bus.Map(clintBase, clintSize, c.Clint)
	c.Bus.WriteBytes(testBase, a.Bytes())
	c.PC = testBase
	for i := 0; i < 50; i++ {
		c.Step()
	}
	if c.X[a0] != interruptBit|irqMTimer {
		t.Errorf("mcause = 0x%x", c.X[a0])
	}
	if c.Clint.Mtime < 100 {
		t.Errorf("wfi should fast-forward mtime, got %d", c.Clint.Mtime)
	}
}
