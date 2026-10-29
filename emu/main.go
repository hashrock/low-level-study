// rvemu: 学習用の小さな RISC-V (RV64IMAC + Zicsr) エミュレータ。
//
//	rvemu run  [flags] prog.elf [args...]   Linux 静的 ELF をユーザモードで実行
//	rvemu boot [flags] kernel.elf           QEMU virt 相当のマシンでカーネルを起動
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"time"
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

	// トレースは大量に出るのでバッファする
	var traceOut *bufio.Writer
	if *trace {
		traceOut = bufio.NewWriterSize(os.Stderr, 1<<16)
		defer traceOut.Flush()
	}

	var cpu *CPU
	switch cmd {
	case "run":
		proc := &Process{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Verbose: *verbose}
		c, err := NewUserMachine(fs.Arg(0), fs.Args()[1:], proc)
		if err != nil {
			fatal(err)
		}
		cpu = c
	case "boot":
		sys, err := NewSystem(fs.Arg(0), os.Stdin, os.Stdout)
		if err != nil {
			fatal(err)
		}
		cpu = sys.CPU
		if *realtime {
			// mtime は 10MHz 相当: 1 tick = 100ns
			cpu.IdleHook = func(ticks uint64) { time.Sleep(time.Duration(min(ticks, 100_000)) * 100) }
		}
	default:
		usage()
	}
	if traceOut != nil {
		cpu.Trace = traceOut
	}

	start := time.Now()
	err := Run(cpu, *maxInsts, *timeout)
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "rvemu:", err)
	os.Exit(1)
}
