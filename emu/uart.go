package main

import "io"

// UART 16550 (QEMU virt では 0x10000000)。ポーリングで使う最低限の実装。
//
//   +0 RBR (読み: 受信データ) / THR (書き: 送信データ)   ※LCR.DLAB=0 のとき
//   +1 IER  割り込み許可 (割り込みは未実装、値を保持するだけ)
//   +2 IIR (読み) / FCR (書き)
//   +3 LCR  ライン制御 (bit7 = DLAB: 1 のとき +0/+1 はボーレート分周値)
//   +4 MCR, +5 LSR (ラインステータス), +6 MSR, +7 SCR
//
// LSR bit0 (DR)   = 受信データあり
// LSR bit5 (THRE) = 送信バッファ空き (このエミュレータでは常に 1)

const (
	uartBase = 0x1000_0000
	uartSize = 0x100

	lsrDataReady = 0x01
	lsrTHREmpty  = 0x20
	lsrTxIdle    = 0x40
)

type UART struct {
	out     io.Writer
	in      chan byte // ホスト入力 (別 goroutine が詰める)
	pending []byte    // 取り出したがまだゲストが読んでいない文字
	regs    [8]uint8
	dll     [2]uint8
}

// NewUART は in から読んだバイトをゲストの受信データにし、送信データを out に書く。
func NewUART(in io.Reader, out io.Writer) *UART {
	u := &UART{out: out, in: make(chan byte, 4096)}
	if in != nil {
		go func() {
			buf := make([]byte, 256)
			for {
				n, err := in.Read(buf)
				for _, b := range buf[:n] {
					u.in <- b
				}
				if err != nil {
					close(u.in)
					return
				}
			}
		}()
	} else {
		close(u.in)
	}
	return u
}

// hasInput はブロックせずに受信データの有無を調べる。
func (u *UART) hasInput() bool {
	if len(u.pending) > 0 {
		return true
	}
	select {
	case b, ok := <-u.in:
		if ok {
			u.pending = append(u.pending, b)
			return true
		}
		u.in = nil // 入力終了。nil チャネルは select で二度と選ばれない
	default:
	}
	return false
}

func (u *UART) dlab() bool { return u.regs[3]&0x80 != 0 }

func (u *UART) Load(off uint64, size int) uint64 {
	switch off {
	case 0:
		if u.dlab() {
			return uint64(u.dll[0])
		}
		if u.hasInput() {
			b := u.pending[0]
			u.pending = u.pending[1:]
			return uint64(b)
		}
		return 0
	case 1:
		if u.dlab() {
			return uint64(u.dll[1])
		}
		return uint64(u.regs[1])
	case 2:
		return 0xc1 // IIR: FIFO 有効, 割り込みなし
	case 5:
		lsr := uint64(lsrTHREmpty | lsrTxIdle)
		if u.hasInput() {
			lsr |= lsrDataReady
		}
		return lsr
	}
	if off < 8 {
		return uint64(u.regs[off])
	}
	return 0
}

func (u *UART) Store(off uint64, size int, v uint64) {
	switch {
	case off == 0 && !u.dlab():
		u.out.Write([]byte{byte(v)})
	case off < 2 && u.dlab():
		u.dll[off] = byte(v)
	case off < 8:
		u.regs[off] = byte(v)
	}
}
