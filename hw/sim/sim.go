// Package sim は Icarus Verilog のシミュレーション (vvp) を動かし、
// 同じ ELF を ../emu (rvemu/rv) で 1 命令ずつ実行して突き合わせる (lockstep 比較)。
//
// テストベンチ (tb/tb.v) は命令が完了するたびに 1 行のコミットトレースを書く:
//
//	C <pc> <命令語> <rd> <rd に書いた値> <ストア番地> <ストアサイズ> <ストア値>
//
// こちらは 1 行読むごとに emu を 1 ステップ進め、
//   - pc と命令語が同じか
//   - トレースの rd 書き込みを反映した「影のレジスタファイル」が emu の x0..x31 と全部一致するか
//   - ストアの番地・サイズ・値が一致するか
//
// を確かめる。最初に食い違った命令で止め、直前の数命令と一緒に報告する。
package sim

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"rvemu/rv"
	"rvhw/loader"
)

// Dir は hw/ ディレクトリ (このファイルの 1 つ上)。
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(file))
}

// RTLFiles は Verilog のソース一覧 (テストベンチ込み)。top を "pipe" にするとパイプライン版。
// (tb_pipe.v は `define PIPE して tb.v を include するだけ)
func RTLFiles(top string) []string {
	d := Dir()
	files, _ := filepath.Glob(filepath.Join(d, "rtl", "*.v"))
	tb := "tb.v"
	if top == "pipe" {
		tb = "tb_pipe.v"
	}
	return append([]string{filepath.Join(d, "tb", tb)}, files...)
}

// Compile は iverilog で .vvp を作る。
func Compile(top, out string) error {
	// ソースをこのプロセスでも一度読んでおく: go test のキャッシュは「テストが開いたファイル」で
	// 判定するので、iverilog (別プロセス) が読むだけだと .v を直してもキャッシュが使われてしまう
	inc, _ := filepath.Glob(filepath.Join(Dir(), "rtl", "*.vh"))
	inc = append(inc, filepath.Join(Dir(), "tb", "tb.v"))
	for _, f := range append(RTLFiles(top), inc...) {
		if _, err := os.ReadFile(f); err != nil {
			return err
		}
	}
	args := append([]string{"-g2005", "-Wall", "-Wno-timescale", "-s", "tb", "-I", filepath.Join(Dir(), "rtl"), "-I", filepath.Join(Dir(), "tb"), "-o", out}, RTLFiles(top)...)
	cmd := exec.Command("iverilog", args...)
	if b, err := cmd.CombinedOutput(); err != nil || len(b) > 0 {
		if err == nil {
			err = fmt.Errorf("warnings")
		}
		return fmt.Errorf("iverilog: %v\n%s", err, b)
	}
	return nil
}

// Options は 1 回の実行の設定。
type Options struct {
	VVP     string   // Compile で作った .vvp
	Files   []string // .s または .c
	Argv    []string // argv[0] を含む (空なら {"prog"})
	Wait    int      // RAM の待ちサイクル (+wait=)
	WorkDir string   // hex などを書く場所
}

// Result は 1 回の実行の結果。
type Result struct {
	Stdout   string // シミュレーションの標準出力 (= プログラムの出力)
	EmuOut   string // emu の出力
	ExitCode int
	Cycles   uint64
	Instret  uint64
}

type commit struct {
	pc, val, saddr, sdata uint64
	inst                  uint32
	rd, ssize             int
	line                  string
}

func parseCommit(l string) (commit, error) {
	f := strings.Fields(l)
	if len(f) != 8 || f[0] != "C" {
		return commit{}, fmt.Errorf("bad trace line %q", l)
	}
	var c commit
	var err error
	h := func(s string) uint64 {
		v, e := strconv.ParseUint(s, 16, 64)
		if e != nil {
			err = e
		}
		return v
	}
	d := func(s string) int {
		v, e := strconv.Atoi(s)
		if e != nil {
			err = e
		}
		return v
	}
	c.pc, c.inst, c.rd, c.val = h(f[1]), uint32(h(f[2])), d(f[3]), h(f[4])
	c.saddr, c.ssize, c.sdata = h(f[5]), d(f[6]), h(f[7])
	c.line = l
	return c, err
}

var summaryRE = regexp.MustCompile(`\[hw\] exit=(-?\d+) cycles=(\d+) instret=(\d+)`)

// Lockstep はプログラムをビルドし、Verilog と emu を 1 命令ずつ突き合わせながら実行する。
func Lockstep(o Options) (*Result, error) {
	if len(o.Argv) == 0 {
		o.Argv = []string{"prog"}
	}
	elfData, err := loader.BuildFiles(o.Files)
	if err != nil {
		return nil, err
	}
	im, err := loader.Load(elfData, o.Argv)
	if err != nil {
		return nil, err
	}
	hex := filepath.Join(o.WorkDir, "prog.hex")
	if err := im.WriteHex(hex); err != nil {
		return nil, err
	}

	// ---- emu: ユーザモードのマシンを作り、初期状態を Verilog 側に合わせる ----
	var emuOut bytes.Buffer
	m, err := rv.NewUserMachine(elfData, o.Argv, &emuOut, nil)
	if err != nil {
		return nil, err
	}
	// スタックは Verilog の RAM (1 MiB) の末尾に置いたものと同じ内容・同じ番地にする
	m.WriteMem(im.StackLo, im.Mem[im.StackLo:])
	m.CPU.X[2] = im.SP
	// MMIO コンソール (Verilog の console.v) に相当する UART を同じ番地に付ける
	m.Bus.Map(rv.UARTBase, 0x100, rv.NewUART(nil, &emuOut))

	// ---- Verilog: トレースはパイプ (子プロセスの fd 3) で受け取る ----
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	args := append([]string{"-n", o.VVP, "+hex=" + hex, "+trace=/dev/fd/3", fmt.Sprintf("+wait=%d", o.Wait)}, im.PlusArgs()...)
	cmd := exec.Command("vvp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.ExtraFiles = []*os.File{pw}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pw.Close()

	cmpErr := compare(bufio.NewReaderSize(pr, 1<<20), m)
	if cmpErr != nil {
		io.Copy(io.Discard, pr) // 子プロセスが書き込みで詰まらないように読み捨てる
	}
	pr.Close()
	werr := cmd.Wait()
	if cmpErr != nil {
		return nil, cmpErr
	}
	if werr != nil {
		return nil, fmt.Errorf("vvp: %v\n%s", werr, stderr.String())
	}
	sm := summaryRE.FindStringSubmatch(stderr.String())
	if sm == nil {
		return nil, fmt.Errorf("simulation did not exit normally:\n%s", stderr.String())
	}
	r := &Result{Stdout: stdout.String(), EmuOut: emuOut.String()}
	r.ExitCode, _ = strconv.Atoi(sm[1])
	r.Cycles, _ = strconv.ParseUint(sm[2], 10, 64)
	r.Instret, _ = strconv.ParseUint(sm[3], 10, 64)

	if !m.CPU.Halted {
		return r, fmt.Errorf("Verilog exited after %d instructions but emu is still running at pc=%x", r.Instret, m.CPU.PC)
	}
	if m.CPU.Err != nil {
		return r, fmt.Errorf("emu error: %v", m.CPU.Err)
	}
	if m.CPU.ExitCode != r.ExitCode {
		return r, fmt.Errorf("exit code: Verilog %d, emu %d", r.ExitCode, m.CPU.ExitCode)
	}
	if r.Stdout != r.EmuOut {
		return r, fmt.Errorf("output differs:\n--- Verilog\n%s\n--- emu\n%s", r.Stdout, r.EmuOut)
	}
	return r, nil
}

// compare はトレースを 1 行ずつ読み、emu を 1 ステップずつ進めて比べる。
func compare(r *bufio.Reader, m *rv.Machine) error {
	shadow := m.CPU.X // Verilog 側のレジスタファイルの写し (初期値は emu と同じ)
	var hist []string // 直前の数命令 (報告用)
	n := 0
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		c, err := parseCommit(strings.TrimSpace(line))
		if err != nil {
			return err
		}
		n++
		fail := func(format string, a ...any) error {
			var b strings.Builder
			fmt.Fprintf(&b, "lockstep: diverged at instruction #%d: %s\n", n, fmt.Sprintf(format, a...))
			fmt.Fprintf(&b, "  直前のコミット (Verilog):\n")
			for _, h := range hist {
				fmt.Fprintf(&b, "    %s\n", h)
			}
			fmt.Fprintf(&b, "  → %s\n", c.line)
			for _, d := range m.DisasmAround(c.pc, 4, 2) {
				mark := "  "
				if d.Addr == c.pc {
					mark = "=>"
				}
				fmt.Fprintf(&b, "  %s %08x: %08x  %s\n", mark, d.Addr, d.Raw, d.Text)
			}
			return fmt.Errorf("%s", b.String())
		}

		if m.CPU.Halted {
			return fail("emu has already exited (code %d)", m.CPU.ExitCode)
		}
		pc := m.CPU.PC
		rawB := m.ReadMem(pc, 4)
		raw := uint32(rawB[0]) | uint32(rawB[1])<<8 | uint32(rawB[2])<<16 | uint32(rawB[3])<<24
		if c.pc != pc {
			return fail("pc: Verilog %016x, emu %016x", c.pc, pc)
		}
		if c.inst != raw {
			return fail("instruction word: Verilog %08x, emu %08x", c.inst, raw)
		}
		// ストアなら、実行前のレジスタから番地・サイズ・値を計算しておく
		var esize int
		var eaddr, edata uint64
		if raw&0x7f == 0x23 {
			imm := uint64(int64(int32(raw)>>25<<5) | int64(raw>>7&0x1f))
			eaddr = m.CPU.X[raw>>15&31] + imm
			esize = 1 << (raw >> 12 & 3)
			edata = m.CPU.X[raw>>20&31]
			if esize < 8 {
				edata &= 1<<(8*esize) - 1
			}
		}
		m.Step()
		if m.CPU.Err != nil {
			return fail("emu error: %v", m.CPU.Err)
		}
		if c.rd != 0 {
			shadow[c.rd] = c.val
		}
		for i := 1; i < 32; i++ {
			if shadow[i] != m.CPU.X[i] {
				return fail("register %s (x%d): Verilog %016x, emu %016x", rv.RegNames[i], i, shadow[i], m.CPU.X[i])
			}
		}
		if esize != c.ssize || eaddr != c.saddr || edata != c.sdata {
			return fail("store: Verilog [%x] size %d = %x, emu [%x] size %d = %x",
				c.saddr, c.ssize, c.sdata, eaddr, esize, edata)
		}
		hist = append(hist, c.line)
		if len(hist) > 6 {
			hist = hist[1:]
		}
	}
}
