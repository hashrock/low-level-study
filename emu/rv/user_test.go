package rv

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ユーザモードランナーのテスト。ミニアセンブラで書いたプログラムを ELF にして実行する。
// `go test -run TestUser -update` で testdata/*.elf を書き出す (手で go run . run できる)。

var update = flag.Bool("update", false, "write example ELF files to testdata/")

// hello: write(1, msg, len); exit(0)
func helloProgram() []byte {
	a := userProgram()
	msg := "Hello, RISC-V!\n"
	a.Emit(addi(a0, zero, 1))
	a.La(a1, "msg")
	a.Emit(addi(a2, zero, int64(len(msg))), addi(a7, zero, sysWrite), ecall)
	a.Exit(0)
	a.Data("msg", []byte(msg))
	return BuildELF(0x10000, a.Bytes(), 0)
}

// sum: 1+2+...+100 を計算し、10 進数に変換して表示する。終了コードは 5050 % 256。
func sumProgram() []byte {
	a := userProgram()
	// t0 = i, t1 = sum
	a.Emit(addi(t0, zero, 1), addi(t1, zero, 0), addi(t2, zero, 101))
	a.Label("loop")
	a.Emit(add(t1, t1, t0), addi(t0, t0, 1))
	a.Branch(4, t0, t2, "loop") // blt t0, t2, loop
	// 10 進変換: バッファの末尾から 1 桁ずつ書く (a1 = 書き込み位置)
	a.La(a1, "buf_end")
	a.Emit(addi(a1, a1, -1), addi(t0, zero, '\n'), encS(opStore, 0, a1, t0, 0)) // "\n"
	a.Emit(addi(t2, zero, 10), add(a3, t1, zero))
	a.Label("digit")
	a.Emit(
		encR(opOp, t0, 7, a3, t2, 1), // remu t0, a3, 10
		encR(opOp, a3, 5, a3, t2, 1), // divu a3, a3, 10
		addi(t0, t0, '0'),
		addi(a1, a1, -1),
		encS(opStore, 0, a1, t0, 0), // sb t0, 0(a1)
	)
	a.Branch(1, a3, zero, "digit") // bnez a3, digit
	a.La(a2, "buf_end")
	a.Emit(encR(opOp, a2, 0, a2, a1, 0x20)) // sub a2, a2, a1 (長さ)
	a.Emit(addi(a0, zero, 1), addi(a7, zero, sysWrite), ecall)
	a.Emit(add(a0, t1, zero), addi(a7, zero, sysExitGroup), ecall) // exit_group(sum)
	a.Data("buf", make([]byte, 24))
	a.Data("buf_end", nil)
	return BuildELF(0x10000, a.Bytes(), 0)
}

// echo: stdin を 1 バイトずつ読み、大文字にして stdout に書く (EOF で終了)。
func upcaseProgram() []byte {
	a := userProgram()
	a.Emit(addi(sp, sp, -16))
	a.Label("loop")
	a.Emit(addi(a0, zero, 0), add(a1, sp, zero), addi(a2, zero, 1), addi(a7, zero, sysRead), ecall)
	a.Branch(0, a0, zero, "eof") // beqz a0, eof
	a.Emit(encI(opLoad, t0, 4, sp, 0), addi(t1, zero, 'a'), addi(t2, zero, 'z'+1))
	a.Branch(6, t0, t1, "out") // bltu t0, 'a', out
	a.Branch(7, t0, t2, "out") // bgeu t0, 'z'+1, out
	a.Emit(addi(t0, t0, -32), encS(opStore, 0, sp, t0, 0))
	a.Label("out")
	a.Emit(addi(a0, zero, 1), add(a1, sp, zero), addi(a2, zero, 1), addi(a7, zero, sysWrite), ecall)
	a.Jal(zero, "loop")
	a.Label("eof")
	a.Exit(0)
	return BuildELF(0x10000, a.Bytes(), 0)
}

// brk: ヒープを 4KiB 伸ばして書き込み、読み戻した値を終了コードにする。
func brkProgram() []byte {
	a := userProgram()
	a.Emit(addi(a0, zero, 0), addi(a7, zero, sysBrk), ecall) // a0 = 現在の brk
	a.Emit(add(t0, a0, zero))
	a.Li(t1, 4096)
	a.Emit(add(a0, a0, t1), addi(a7, zero, sysBrk), ecall) // brk(cur+4096)
	a.Emit(addi(t1, zero, 42), sd(t1, t0, 8), ld(a0, t0, 8))
	a.Emit(addi(a7, zero, sysExit), ecall)
	return BuildELF(0x10000, a.Bytes(), 0)
}

func runUser(t *testing.T, name string, elf []byte, stdin string) (string, int, *CPU) {
	t.Helper()
	dir := t.TempDir()
	if *update {
		dir = "../testdata"
		os.MkdirAll(dir, 0o755)
	}
	path := filepath.Join(dir, name+".elf")
	if err := os.WriteFile(path, elf, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	m, err := NewUserMachine(elf, []string{path}, &out, strings.NewReader(stdin))
	if err != nil {
		t.Fatal(err)
	}
	exited, code, err := m.Run(10_000_000)
	if err != nil || !exited {
		t.Fatalf("%s: exited=%v err=%v", name, exited, err)
	}
	return out.String(), code, m.CPU
}

func TestUserHello(t *testing.T) {
	out, code, _ := runUser(t, "hello", helloProgram(), "")
	if out != "Hello, RISC-V!\n" || code != 0 {
		t.Errorf("out=%q code=%d", out, code)
	}
}

func TestUserSum(t *testing.T) {
	out, code, c := runUser(t, "sum", sumProgram(), "")
	if out != "5050\n" || code != 5050 {
		t.Errorf("out=%q code=%d", out, code)
	}
	t.Logf("sum: %d instructions", c.Steps)
}

func TestUserUpcase(t *testing.T) {
	out, _, _ := runUser(t, "upcase", upcaseProgram(), "hello, World 123\n")
	if out != "HELLO, WORLD 123\n" {
		t.Errorf("out=%q", out)
	}
}

func TestUserBrk(t *testing.T) {
	if _, code, _ := runUser(t, "brk", brkProgram(), ""); code != 42 {
		t.Errorf("code=%d", code)
	}
}

// スタックに argc / argv が Linux と同じ形で積まれている。
func TestUserStack(t *testing.T) {
	m, err := NewUserMachine(helloProgram(), []string{"p.elf", "foo", "bar"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := m.CPU
	sp := c.X[2]
	argc, _ := c.Bus.Load(sp, 8)
	argv2, _ := c.Bus.Load(sp+24, 8)
	s, _ := c.Bus.ReadBytes(argv2, 3)
	if sp%16 != 0 || argc != 3 || string(s) != "bar" {
		t.Errorf("sp=0x%x argc=%d argv[2]=%q", sp, argc, s)
	}
}

// 不正命令はユーザモードランナーではエラー終了になる。
func TestUserIllegalInstruction(t *testing.T) {
	a := userProgram()
	a.Emit(0xffffffff)
	m, _ := NewUserMachine(BuildELF(0x10000, a.Bytes(), 0), nil, nil, nil)
	_, _, err := m.Run(1000)
	if err == nil || !strings.Contains(err.Error(), "illegal instruction") {
		t.Errorf("err = %v", err)
	}
}

// .bss だけのセグメント (filesz=0) もロードでき、0 で埋まっている。
func TestUserBssOnlySegment(t *testing.T) {
	a := userProgram()
	a.Li(t0, 0x11000) // 2 つ目のセグメント (.bss) の先頭
	a.Emit(ld(a0, t0, 8), addi(a0, a0, 7), sd(a0, t0, 8), ld(a0, t0, 8))
	a.Emit(addi(a7, zero, sysExit), ecall)
	if _, code, _ := runUser(t, "bss", BuildELF(0x10000, a.Bytes(), 0x2000), ""); code != 7 {
		t.Errorf("code=%d", code)
	}
}
