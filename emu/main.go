// rvemu: 学習用の小さな RISC-V (RV64IMAC + Zicsr) エミュレータ。
//
//	rvemu run  [flags] prog.elf [args...]   Linux 静的 ELF をユーザモードで実行
//	rvemu boot [flags] kernel.elf           QEMU virt 相当のマシンでカーネルを起動
//
// エミュレータ本体は rvemu/rv パッケージ (このファイルは CLI だけ)。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"time"

	"rvemu/rv"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  rvemu run  [--trace] [--stats] [--max-insts N] prog.elf [args...]
  rvemu boot [--trace] [--stats] [--max-insts N] [--timeout 10s] [--realtime] kernel.elf`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	trace := fs.Bool("trace", false, "print every executed instruction to stderr")
	stats := fs.Bool("stats", false, "print instruction count and speed at exit")
	maxInsts := fs.Uint64("max-insts", 0, "stop after N instructions (0 = unlimited)")
	timeout := fs.Duration("timeout", 0, "stop after this wall-clock time (0 = unlimited)")
	realtime := fs.Bool("realtime", false, "boot: sleep in WFI so the guest timer follows wall-clock time")
	verbose := fs.Bool("v", false, "report unimplemented syscalls")
	fs.Parse(os.Args[2:])
	if fs.NArg() < 1 {
		usage()
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal(err)
	}

	// トレースは大量に出るのでバッファする
	var traceOut *bufio.Writer
	if *trace {
		traceOut = bufio.NewWriterSize(os.Stderr, 1<<16)
		defer traceOut.Flush()
	}

	var m *rv.Machine
	switch cmd {
	case "run":
		m, err = rv.NewUserMachine(data, fs.Args(), os.Stdout, os.Stdin)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", fs.Arg(0), err))
		}
		m.Proc.Stderr = os.Stderr
		m.Proc.Verbose = *verbose
	case "boot":
		m, err = rv.NewVirtMachine(data, os.Stdout, os.Stdin, nil)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", fs.Arg(0), err))
		}
		if *realtime {
			// mtime は 10MHz 相当: 1 tick = 100ns
			m.CPU.IdleHook = func(ticks uint64) { time.Sleep(time.Duration(min(ticks, 100_000)) * 100) }
		}
	default:
		usage()
	}
	cpu := m.CPU
	if traceOut != nil {
		cpu.Trace = traceOut
	}

	start := time.Now()
	err = run(m, *maxInsts, *timeout)
	elapsed := time.Since(start)
	if traceOut != nil {
		traceOut.Flush()
	}
	if *stats {
		mips := float64(cpu.Steps) / elapsed.Seconds() / 1e6
		fmt.Fprintf(os.Stderr, "\n[rvemu] %d instructions in %v (%.1f MIPS), exit code %d\n",
			cpu.Steps, elapsed.Round(time.Millisecond), mips, cpu.ExitCode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[rvemu] stopped: %v\n", err)
		cpu.DumpRegs(os.Stderr)
		if cpu.ExitCode == 0 {
			cpu.ExitCode = 1
		}
	}
	os.Exit(cpu.ExitCode)
}

// run は止まるまで (または上限に達するまで) 実行する。時刻チェックは重いので 64K 命令ごと。
func run(m *rv.Machine, maxSteps uint64, timeout time.Duration) error {
	start := time.Now()
	for {
		if exited, _, err := m.Run(1 << 16); exited {
			return err
		}
		if maxSteps > 0 && m.CPU.Steps >= maxSteps {
			return rv.ErrStepLimit
		}
		if timeout > 0 && time.Since(start) > timeout {
			return fmt.Errorf("timeout")
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "rvemu:", err)
	os.Exit(1)
}
