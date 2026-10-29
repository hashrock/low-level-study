package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// システムモード (boot) のテスト。M モードの小さな「カーネル」を手でアセンブルし、
// UART 出力・UART 入力・タイマ割り込み・U モードからの ecall・電源断を確認する。
func miniKernel() []byte {
	a := NewAsm(ramBase + elfHeaderSize)
	a.La(t0, "trap")
	a.Emit(csrw(csrMtvec, t0))
	// UART に "OK\n" を出す
	a.Li(a3, uartBase)
	for _, ch := range "OK\n" {
		a.Emit(addi(t1, zero, int64(ch)), encS(opStore, 0, a3, t1, 0))
	}
	// UART から 1 文字読み (LSR.DR をポーリング)、+1 して送り返す
	a.Label("rx")
	a.Emit(encI(opLoad, t1, 4, a3, 5), encI(opOpImm, t1, 7, t1, 1)) // lbu t1, 5(a3); andi t1, t1, 1
	a.Branch(0, t1, zero, "rx")
	a.Emit(encI(opLoad, t1, 4, a3, 0), addi(t1, t1, 1), encS(opStore, 0, a3, t1, 0))
	// タイマ: mtimecmp = mtime + 1000, MTIE を有効にして U モードへ
	a.Li(t0, clintBase)
	a.Li(t1, 0xbff8)
	a.Emit(add(t1, t0, t1), ld(t2, t1, 0), addi(t2, t2, 1000))
	a.Li(t1, 0x4000)
	a.Emit(add(t1, t0, t1), sd(t2, t1, 0))
	a.Emit(addi(t1, zero, 1<<7), csrw(csrMie, t1))
	a.Emit(addi(t1, zero, 1<<7), csrrs(0, csrMstatus, t1)) // MPIE=1 → mret 後に割り込み有効
	a.La(t0, "user")
	a.Emit(csrw(csrMepc, t0), mret)

	// U モード: タイマ割り込みが来るまでループ (2 命令/周 なので 1000 tick で約 500 周)
	a.Label("user")
	a.Emit(addi(a0, a0, 1))
	a.Jal(zero, "user")

	// トラップハンドラ: タイマ割り込みなら '!' を出して終了
	a.Label("trap")
	a.Emit(csrr(t0, csrMcause))
	a.Branch(5, t0, zero, "bad") // bge t0, zero (最上位ビットが 0 = 例外) → bad
	a.Emit(addi(t1, zero, '!'), encS(opStore, 0, a3, t1, 0))
	a.Li(t0, testDevBase)
	a.Li(t1, 0x5555)
	a.Emit(encS(opStore, 2, t0, t1, 0)) // sw → 電源断
	a.Label("bad")
	a.Li(t0, testDevBase)
	a.Li(t1, 0x3333|5<<16)
	a.Emit(encS(opStore, 2, t0, t1, 0)) // FAIL, exit code 5
	return BuildELF(ramBase, a.Bytes(), 0)
}

func TestBootMiniKernel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.elf")
	os.WriteFile(path, miniKernel(), 0o755)
	var out bytes.Buffer
	sys, err := NewSystem(path, strings.NewReader("A"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(sys.CPU, 1_000_000, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if out.String() != "OK\nB!" || sys.CPU.ExitCode != 0 {
		t.Errorf("out=%q exit=%d", out.String(), sys.CPU.ExitCode)
	}
	if sys.CPU.X[a0] < 400 {
		t.Errorf("user loop ran only %d times before the timer", sys.CPU.X[a0])
	}
}

// ../os のカーネルが既にビルドされていれば、シェルまで起動してコマンドを流す。
// RVEMU_KERNEL=path/to/kernel.elf で場所を指定できる。
func TestBootZigOS(t *testing.T) {
	path := os.Getenv("RVEMU_KERNEL")
	if path == "" {
		path = "../os/zig-out/bin/kernel.elf"
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("zigos kernel not built:", path)
	}
	var out bytes.Buffer
	sys, err := NewSystem(path, strings.NewReader("echo hello from rvemu\rls\rhalt\r"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(sys.CPU, 500_000_000, 20*time.Second); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"zigos:/$ ", "hello from rvemu", "etc/", "bye."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
