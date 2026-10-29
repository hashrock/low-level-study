package main

import (
	"fmt"
	"io"
	"os"
)

// ユーザモードランナー用の Linux システムコール (RISC-V の番号)。
// 呼び出し規約: a7 = 番号, a0..a5 = 引数, 戻り値は a0 (エラーは -errno)。

const (
	sysIoctl         = 29
	sysRead          = 63
	sysWrite         = 64
	sysWritev        = 66
	sysExit          = 93
	sysExitGroup     = 94
	sysSetTidAddress = 96
	sysClockGettime  = 113
	sysSigaction     = 134
	sysSigprocmask   = 135
	sysGetpid        = 172
	sysGettid        = 178
	sysBrk           = 214
	sysMunmap        = 215
	sysMmap          = 222
	sysMprotect      = 226

	errNOENT = 2
	errBADF  = 9
	errNOMEM = 12
	errFAULT = 14
	errINVAL = 22
	errNOTTY = 25
	errNOSYS = 38
	pageSize = 4096
)

// Process は 1 つのユーザプロセス (ホスト側から見た状態)。
type Process struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	brk, brkStart  uint64 // ヒープ (brk) の現在位置と開始位置
	mmapNext       uint64 // 匿名 mmap の次の割り当て先
	mmapLimit      uint64
	Verbose        bool // 未実装システムコールを stderr に報告
}

func pageUp(v uint64) uint64 { return (v + pageSize - 1) &^ (pageSize - 1) }

func (p *Process) fd(n uint64) (io.Reader, io.Writer) {
	switch n {
	case 0:
		return p.Stdin, nil
	case 1:
		return nil, p.Stdout
	case 2:
		return nil, p.Stderr
	}
	return nil, nil
}

// Handle は ecall 1 回分を処理する。CPU.Syscall に登録して使う。
func (p *Process) Handle(c *CPU) {
	a := c.X[10:16] // a0..a5
	ret := p.dispatch(c, c.X[17], a[0], a[1], a[2])
	if !c.Halted {
		c.X[10] = uint64(ret)
	}
}

func (p *Process) dispatch(c *CPU, num, a0, a1, a2 uint64) int64 {
	bus := c.Bus
	switch num {
	case sysWrite:
		_, w := p.fd(a0)
		if w == nil {
			return -errBADF
		}
		buf, ok := bus.ReadBytes(a1, a2)
		if !ok {
			return -errFAULT
		}
		n, _ := w.Write(buf)
		return int64(n)

	case sysWritev: // struct iovec { void *base; size_t len; } の配列
		_, w := p.fd(a0)
		if w == nil {
			return -errBADF
		}
		total := int64(0)
		for i := uint64(0); i < a2; i++ {
			base, ok1 := bus.Load(a1+16*i, 8)
			n, ok2 := bus.Load(a1+16*i+8, 8)
			buf, ok3 := bus.ReadBytes(base, n)
			if !ok1 || !ok2 || !ok3 {
				return -errFAULT
			}
			w.Write(buf)
			total += int64(n)
		}
		return total

	case sysRead:
		r, _ := p.fd(a0)
		if r == nil {
			return -errBADF
		}
		buf, ok := bus.ReadBytes(a1, a2)
		if !ok {
			return -errFAULT
		}
		n, err := r.Read(buf)
		if err != nil && err != io.EOF {
			return -errINVAL
		}
		return int64(n)

	case sysExit, sysExitGroup:
		c.Halt(int(int32(a0)), nil)
		return 0

	case sysBrk:
		// brk(0) は現在位置を返す。それ以外は範囲内なら移動する
		if a0 >= p.brkStart && a0 < p.mmapNext {
			p.brk = a0
		}
		return int64(p.brk)

	case sysMmap: // 匿名マッピングのみ: 単純なバンプアロケータ (munmap しても再利用しない)
		const mapAnonymous = 0x20
		flags := c.X[13]
		if flags&mapAnonymous == 0 {
			return -errNOSYS
		}
		size := pageUp(a1)
		if p.mmapNext+size > p.mmapLimit {
			return -errNOMEM
		}
		addr := p.mmapNext
		p.mmapNext += size
		zero := make([]byte, size)
		bus.WriteBytes(addr, zero)
		return int64(addr)

	case sysMunmap, sysMprotect, sysSigaction, sysSigprocmask:
		return 0
	case sysSetTidAddress, sysGetpid, sysGettid:
		return 1
	case sysIoctl:
		return -errNOTTY // 端末ではないことにする
	case sysClockGettime: // struct timespec { sec; nsec } を命令数ベースで埋める
		ns := c.Steps * 10 // 100MHz の CPU とみなす
		bus.Store(a1, 8, ns/1e9)
		bus.Store(a1+8, 8, ns%1e9)
		return 0
	}
	if p.Verbose {
		fmt.Fprintf(os.Stderr, "[rvemu] unimplemented syscall %d at pc=0x%x\n", num, c.PC)
	}
	return -errNOSYS
}
