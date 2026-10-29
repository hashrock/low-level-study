package main

// CSR (Control and Status Register) の読み書き。
// CSR は 12 ビットの番号で指定する 4096 個のレジスタ空間。
//   番号の [9:8] ビット = アクセスに必要な最低特権レベル
//   番号の [11:10] ビットが 0b11 なら読み出し専用

// 特権レベル
const (
	PrivU uint64 = 0
	PrivS uint64 = 1
	PrivM uint64 = 3
)

// よく使う CSR の番号
const (
	csrSstatus  = 0x100
	csrSie      = 0x104
	csrStvec    = 0x105
	csrSscratch = 0x140
	csrSepc     = 0x141
	csrScause   = 0x142
	csrStval    = 0x143
	csrSip      = 0x144
	csrSatp     = 0x180

	csrMstatus  = 0x300
	csrMisa     = 0x301
	csrMedeleg  = 0x302
	csrMideleg  = 0x303
	csrMie      = 0x304
	csrMtvec    = 0x305
	csrMscratch = 0x340
	csrMepc     = 0x341
	csrMcause   = 0x342
	csrMtval    = 0x343
	csrMip      = 0x344

	csrCycle   = 0xc00
	csrTime    = 0xc01
	csrInstret = 0xc02
	csrMcycle  = 0xb00
	csrMinstr  = 0xb02
	csrMhartid = 0xf14
)

// mstatus のビット
const (
	mstatusSIE  = 1 << 1
	mstatusMIE  = 1 << 3
	mstatusSPIE = 1 << 5
	mstatusMPIE = 1 << 7
	mstatusSPP  = 1 << 8
	mstatusMPP  = 3 << 11
	mstatusMPRV = 1 << 17
	mstatusSUM  = 1 << 18
	mstatusMXR  = 1 << 19
	mstatusUXL  = 3 << 32
	mstatusSXL  = 3 << 34
	// sstatus から見える (= S モードが触れる) ビット
	sstatusMask = mstatusSIE | mstatusSPIE | mstatusSPP | mstatusSUM | mstatusMXR | mstatusUXL
)

// mip / mie のビット (割り込み番号 = ビット位置)
const (
	irqSSoft  = 1
	irqMSoft  = 3
	irqSTimer = 5
	irqMTimer = 7
	irqSExt   = 9
	irqMExt   = 11
)

var csrNames = map[uint32]string{
	csrSstatus: "sstatus", csrSie: "sie", csrStvec: "stvec", csrSscratch: "sscratch",
	csrSepc: "sepc", csrScause: "scause", csrStval: "stval", csrSip: "sip", csrSatp: "satp",
	csrMstatus: "mstatus", csrMisa: "misa", csrMedeleg: "medeleg", csrMideleg: "mideleg",
	csrMie: "mie", csrMtvec: "mtvec", csrMscratch: "mscratch", csrMepc: "mepc",
	csrMcause: "mcause", csrMtval: "mtval", csrMip: "mip", csrCycle: "cycle", csrTime: "time",
	csrInstret: "instret", csrMcycle: "mcycle", csrMinstr: "minstret", csrMhartid: "mhartid",
	0x3a0: "pmpcfg0", 0x3b0: "pmpaddr0",
}

func csrName(n uint32) string {
	if s, ok := csrNames[n]; ok {
		return s
	}
	return sprintf("csr0x%03x", n)
}

func (c *CPU) resetCSRs() {
	c.csr[csrMstatus] = 2<<32 | 2<<34 // UXL=SXL=64bit
	// misa: MXL=2 (64bit), 拡張 A C I M S U
	c.csr[csrMisa] = 2<<62 | 1<<0 | 1<<2 | 1<<8 | 1<<12 | 1<<18 | 1<<20
}

// csrAccessOK は現在の特権レベルでその CSR を触れるか判定する。
func (c *CPU) csrAccessOK(n uint32, write bool) bool {
	if uint64(n>>8&3) > c.Priv {
		return false
	}
	if write && n>>10 == 3 {
		return false
	}
	return true
}

// readCSR は CSR の値を返す。いくつかの CSR は他の値の「見え方の違い」に過ぎない。
func (c *CPU) readCSR(n uint32) uint64 {
	switch n {
	case csrSstatus:
		return c.csr[csrMstatus] & sstatusMask
	case csrSie:
		return c.csr[csrMie] & c.csr[csrMideleg]
	case csrSip:
		return c.csr[csrMip] & c.csr[csrMideleg]
	case csrCycle, csrMcycle, csrInstret, csrMinstr:
		return c.Steps
	case csrTime:
		if c.Clint != nil {
			return c.Clint.Mtime
		}
		return c.Steps
	}
	return c.csr[n]
}

// writeCSR は CSR に書き込む。書き込めないビットはマスクで守る (WARL)。
func (c *CPU) writeCSR(n uint32, v uint64) {
	switch n {
	case csrSstatus:
		c.csr[csrMstatus] = c.csr[csrMstatus]&^sstatusMask | v&sstatusMask&^mstatusUXL
	case csrMstatus:
		keep := uint64(mstatusUXL | mstatusSXL) // 64bit 固定
		c.csr[csrMstatus] = c.csr[csrMstatus]&keep | v&^keep
	case csrSie:
		d := c.csr[csrMideleg]
		c.csr[csrMie] = c.csr[csrMie]&^d | v&d
	case csrSip:
		d := c.csr[csrMideleg] & (1 << irqSSoft) // S モードが書けるのは SSIP だけ
		c.csr[csrMip] = c.csr[csrMip]&^d | v&d
	case csrMip:
		// MTIP / MSIP は CLINT が決める。ソフトが書けるのは S レベルのビットだけ
		w := uint64(1<<irqSSoft | 1<<irqSTimer | 1<<irqSExt)
		c.csr[csrMip] = c.csr[csrMip]&^w | v&w
	case csrSatp:
		// Sv39 などのページングは未実装。WARL なので Bare (MODE=0) 以外の書き込みは無視
		if v>>60 == 0 {
			c.csr[n] = v
		}
	case csrMisa, csrMhartid:
		// 書き込み無視
	case csrMepc, csrSepc:
		c.csr[n] = v &^ 1
	default:
		c.csr[n] = v
	}
}
