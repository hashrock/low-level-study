package rv

import "math"

// CLINT (Core Local Interruptor): タイマ割り込みとソフトウェア割り込みを出すデバイス。
// QEMU virt では 0x02000000 に置かれる。
//
//   +0x0000 msip     (4 バイト) bit0 を 1 にするとソフトウェア割り込み (MSIP)
//   +0x4000 mtimecmp (8 バイト) mtime >= mtimecmp の間タイマ割り込み (MTIP)
//   +0xbff8 mtime    (8 バイト) 時刻カウンタ (QEMU では 10MHz)
//
// このエミュレータでは mtime は「1 命令ごとに 1 増える」。実時間とは無関係なので
// 実行は決定的になる (同じ入力なら毎回同じ結果)。

const (
	clintBase = 0x0200_0000
	clintSize = 0x10000
)

type CLINT struct {
	Msip     uint64
	Mtimecmp uint64
	Mtime    uint64
}

func NewCLINT() *CLINT { return &CLINT{Mtimecmp: math.MaxUint64} }

func (c *CLINT) Tick()              { c.Mtime++ }
func (c *CLINT) TimerPending() bool { return c.Mtime >= c.Mtimecmp }

func (c *CLINT) Load(off uint64, size int) uint64 {
	switch {
	case off < 4:
		return regRead(c.Msip, off, size)
	case off >= 0x4000 && off < 0x4008:
		return regRead(c.Mtimecmp, off, size)
	case off >= 0xbff8 && off < 0xc000:
		return regRead(c.Mtime, off, size)
	}
	return 0
}

func (c *CLINT) Store(off uint64, size int, v uint64) {
	switch {
	case off < 4:
		c.Msip = regWrite(c.Msip, off, size, v) & 1
	case off >= 0x4000 && off < 0x4008:
		c.Mtimecmp = regWrite(c.Mtimecmp, off, size, v)
	case off >= 0xbff8 && off < 0xc000:
		c.Mtime = regWrite(c.Mtime, off, size, v)
	}
}
