package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// マシンの組み立て。2 種類ある:
//
//  1. ユーザモードランナー (run): Linux の静的 ELF を U モードで実行し、
//     ecall はホスト側 (syscall.go) で処理する。カーネルは存在しない。
//  2. システムモード (boot): QEMU virt マシン相当。M モードで 0x80000000 から起動し、
//     ゲストのカーネルがトラップを処理する。

// ---- ユーザモード ----

const userStackSize = 8 << 20

// NewUserMachine は ELF をロードし、Linux と同じ形のスタック (argc/argv/envp/auxv) を作る。
//
//	メモリ配置 (RAM は 0 番地から):
//	  [ELF セグメント][brk ヒープ 32MiB →][mmap 領域 →]......[← スタック 8MiB]
func NewUserMachine(path string, args []string, proc *Process) (*CPU, error) {
	f, info, err := ReadELF(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	brkStart := pageUp(info.MaxAddr)
	ramSize := brkStart + 128<<20
	bus := NewBus(0, ramSize)
	if err := LoadELF(f, info, bus); err != nil {
		return nil, err
	}
	proc.brkStart, proc.brk = brkStart, brkStart
	proc.mmapNext = brkStart + 32<<20
	proc.mmapLimit = ramSize - userStackSize

	c := NewCPU(bus)
	c.Priv = PrivU
	c.UserOnly = true
	c.Syscall = proc.Handle
	c.PC = info.Entry
	c.X[2] = setupStack(bus, ramSize, append([]string{path}, args...), info)
	return c, nil
}

// setupStack はスタック先頭から下に向かって引数文字列と各種ポインタを積む。
//
//	sp -> argc
//	      argv[0] ... argv[n-1], NULL
//	      envp: NULL
//	      auxv: (AT_PHDR, x) (AT_PHENT, 56) (AT_PHNUM, n) (AT_PAGESZ, 4096) (AT_ENTRY, e)
//	            (AT_RANDOM, ptr) (AT_NULL, 0)
//	      ... 文字列の実体
func setupStack(bus *Bus, top uint64, args []string, info *LoadedELF) uint64 {
	sp := top
	push := func(data []byte) uint64 {
		sp -= uint64(len(data))
		bus.WriteBytes(sp, data)
		return sp
	}
	random := push([]byte("rvemu-random-16b")) // AT_RANDOM 用の 16 バイト
	var argv []uint64
	for _, a := range args {
		argv = append(argv, push(append([]byte(a), 0)))
	}

	words := []uint64{uint64(len(args))}
	words = append(words, argv...)
	words = append(words, 0) // argv 終端
	words = append(words, 0) // envp 終端 (環境変数なし)
	words = append(words, 3, info.Phdr, 4, 56, 5, info.Phnum, 6, pageSize, 9, info.Entry, 25, random, 0, 0)

	sp -= uint64(8 * len(words))
	sp &^= 15 // ABI: sp は 16 バイト境界
	for i, w := range words {
		bus.Store(sp+uint64(8*i), 8, w)
	}
	return sp
}

// ---- システムモード (QEMU virt 相当) ----

// メモリマップ
const (
	testDevBase = 0x0010_0000 // sifive_test (電源断)
	ramBase     = 0x8000_0000
	ramSize     = 128 << 20
)

type System struct {
	CPU   *CPU
	UART  *UART
	CLINT *CLINT
}

func NewSystem(kernel string, stdin io.Reader, stdout io.Writer) (*System, error) {
	bus := NewBus(ramBase, ramSize)
	cpu := NewCPU(bus)
	s := &System{CPU: cpu, UART: NewUART(stdin, stdout), CLINT: NewCLINT()}
	cpu.Clint = s.CLINT
	bus.Map(testDevBase, 0x1000, &TestDevice{cpu})
	bus.Map(clintBase, clintSize, s.CLINT)
	bus.Map(uartBase, uartSize, s.UART)

	f, info, err := ReadELF(kernel)
	if err == nil {
		defer f.Close()
		if err := LoadELF(f, info, bus); err != nil {
			return nil, err
		}
		cpu.PC = info.Entry
	} else {
		// ELF でなければ生のバイナリとして 0x80000000 に置く
		data, rerr := os.ReadFile(kernel)
		if rerr != nil {
			return nil, rerr
		}
		if len(data) < 4 || binary.LittleEndian.Uint32(data) == 0x464c457f {
			return nil, err // ELF だが壊れている
		}
		if !bus.WriteBytes(ramBase, data) {
			return nil, errors.New("kernel image too large")
		}
		cpu.PC = ramBase
	}
	// QEMU と同じく a0 = hartid, a1 = デバイスツリーのアドレス (ここでは無し)
	cpu.X[10], cpu.X[11] = 0, 0
	return s, nil
}

// ---- 実行ループ ----

var ErrTimeout = errors.New("timeout")
var ErrStepLimit = errors.New("instruction limit reached")

// Run は CPU が止まるまで (または上限に達するまで) 命令を実行する。
func Run(c *CPU, maxSteps uint64, timeout time.Duration) error {
	start := time.Now()
	for !c.Halted {
		// 時刻チェックは重いので 64K 命令ごと
		for i := 0; i < 1<<16 && !c.Halted; i++ {
			c.Step()
		}
		if maxSteps > 0 && c.Steps >= maxSteps {
			return ErrStepLimit
		}
		if timeout > 0 && time.Since(start) > timeout {
			return ErrTimeout
		}
	}
	return c.Err
}

func (c *CPU) DumpRegs(w io.Writer) {
	fmt.Fprintf(w, "pc=%016x priv=%c steps=%d\n", c.PC, "USHM"[c.Priv], c.Steps)
	for i := 0; i < 32; i++ {
		fmt.Fprintf(w, "%-4s=%016x", regNames[i], c.X[i])
		if i%4 == 3 {
			fmt.Fprintln(w)
		} else {
			fmt.Fprint(w, "  ")
		}
	}
	fmt.Fprintf(w, "mstatus=%x mcause=%x mepc=%x mtval=%x\n",
		c.csr[csrMstatus], c.csr[csrMcause], c.csr[csrMepc], c.csr[csrMtval])
}
