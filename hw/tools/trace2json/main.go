// trace2json: デモプログラムを本物の Verilog シミュレーション (Icarus Verilog) で動かし、
// 波形 (VCD) を「1 サイクル 1 行」の JSON にする。web/hw のページがこれを再生する。
//
//	go run ./tools/trace2json -o ../web/hw/traces            demos/*.s を両方のコアで
//	go run ./tools/trace2json -o /tmp/t -demo arith -core single
//
// 出力:
//
//	<demo>-single.json / <demo>-pipe.json  … 1 本のトレース (下の Trace 型)
//	index.json                              … デモの一覧とサイクル数
//
// 値は「各クロックの立ち上がりエッジ直前」(= そのサイクルで落ち着いた値) を
// 先頭の 0 を省いた 16 進文字列で持つ (64 ビットは JS の number に入らないため)。
// レジスタファイル (VCD には配列の中身が出ない) は、書き込みポート (rf_we/rf_rd/rf_wdata) を
// エッジごとに適用して作り直し、tb.v が引き出している x_ra/x_sp/x_a0… と毎サイクル照合する。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"rvasm/rvasm"
	"rvemu/rv"
	"rvhw/loader"
	"rvhw/sim"
	"rvhw/vcd"
)

// Demo は 1 本のデモプログラム。
type Demo struct {
	Name  string `json:"name"`
	File  string `json:"file"` // hw/ からの相対パス
	Title string `json:"title"`
	Wait  int    `json:"wait"` // RAM の待ちサイクル
	Focus string `json:"focus"`
}

// Demos は web/hw で見せるデモ (どれも両方のコアで動かす)。Focus は最初に表示するコア。
var Demos = []Demo{
	{"arith", "demos/arith.s", "演算: addi a0, a0, -1", 0, "single"},
	{"loadstore", "demos/loadstore.s", "ロードとストア", 0, "single"},
	{"branch", "demos/branch.s", "条件分岐 (成立 / 不成立)", 0, "single"},
	{"call", "demos/call.s", "jal / jalr (呼び出しと戻り)", 0, "single"},
	{"mmio", "demos/mmio.s", "MMIO コンソール", 0, "single"},
	{"stall", "demos/stall.s", "RAM の待ち (wait=2) でストール", 2, "single"},
	{"loaduse", "demos/loaduse.s", "load-use ハザード (バブル)", 0, "pipe"},
	{"forward", "demos/forward.s", "フォワーディング", 0, "pipe"},
	{"flush", "demos/flush.s", "分岐成立でフラッシュ", 0, "pipe"},
	{"loop", "demos/loop.s", "ループ: 1 + 2 + … + 10", 0, "pipe"},
}

// MaxCycles は 1 本のトレースの上限 (JSON を小さく保つ)。
const MaxCycles = 200

// Sig は JSON に出す信号 1 本。
type Sig struct {
	N string `json:"n"` // 表示名
	W int    `json:"w"` // ビット幅
	G string `json:"g"` // グループ (fetch / decode / ...)
}

type sigSpec struct {
	path, name, group string
}

// 単一サイクル版で記録する信号 (VCD の階層名の末尾, 表示名, グループ)
var singleSigs = []sigSpec{
	{"dut.core.pc", "pc", "fetch"},
	{"dut.core.inst", "inst", "fetch"},
	{"dut.core.pc_plus4", "pc_plus4", "fetch"},
	{"dut.core.rd", "rd", "decode"},
	{"dut.core.rs1", "rs1", "decode"},
	{"dut.core.rs2", "rs2", "decode"},
	{"dut.core.funct3", "funct3", "decode"},
	{"dut.core.reg_we", "reg_we", "decode"},
	{"dut.core.a_sel", "a_sel", "decode"},
	{"dut.core.b_sel", "b_sel", "decode"},
	{"dut.core.alu_op", "alu_op", "decode"},
	{"dut.core.is_word", "is_word", "decode"},
	{"dut.core.is_muldiv", "is_muldiv", "decode"},
	{"dut.core.imm_sel", "imm_sel", "decode"},
	{"dut.core.wb_sel", "wb_sel", "decode"},
	{"dut.core.mem_read", "mem_read", "decode"},
	{"dut.core.mem_write", "mem_write", "decode"},
	{"dut.core.branch", "branch", "decode"},
	{"dut.core.jump", "jump", "decode"},
	{"dut.core.jalr", "jalr", "decode"},
	{"dut.core.ecall", "ecall", "decode"},
	{"dut.core.illegal", "illegal", "decode"},
	{"dut.core.imm", "imm", "decode"},
	{"dut.core.rs1_val", "rs1_val", "regfile"},
	{"dut.core.rs2_val", "rs2_val", "regfile"},
	{"dut.core.alu_a", "alu_a", "execute"},
	{"dut.core.alu_b", "alu_b", "execute"},
	{"dut.core.alu_y", "alu_y", "execute"},
	{"dut.core.md_y", "md_y", "execute"},
	{"dut.core.ex_y", "ex_y", "execute"},
	{"dut.core.br_taken", "br_taken", "execute"},
	{"dut.core.redirect", "redirect", "execute"},
	{"dut.core.next_pc", "next_pc", "execute"},
	{"dut.core.d_valid", "d_valid", "memory"},
	{"dut.core.d_we", "d_we", "memory"},
	{"dut.core.d_addr", "d_addr", "memory"},
	{"dut.core.d_wdata", "d_wdata", "memory"},
	{"dut.core.d_wstrb", "d_wstrb", "memory"},
	{"dut.core.d_rdata", "d_rdata", "memory"},
	{"dut.core.d_ready", "d_ready", "memory"},
	{"dut.core.load_val", "load_val", "memory"},
	{"dut.ram_valid", "ram_valid", "memory"},
	{"dut.con_valid", "con_valid", "memory"},
	{"dut.u_ram.cnt", "ram_cnt", "memory"},
	{"dut.tx_valid", "tx_valid", "memory"},
	{"dut.tx_data", "tx_data", "memory"},
	{"dut.core.wb_val", "wb_val", "writeback"},
	{"dut.core.stall", "stall", "writeback"},
	{"dut.core.rf_we", "rf_we", "writeback"},
	{"dut.core.rf_rd", "rf_rd", "writeback"},
	{"dut.core.rf_wdata", "rf_wdata", "writeback"},
	{"dut.core.commit", "commit", "writeback"},
	{"tb.sys_done", "sys_done", "writeback"},
	{"tb.sys_ret", "sys_ret", "writeback"},
}

// パイプライン版で記録する信号
var pipeSigs = []sigSpec{
	{"dut.core.pc", "pc", "IF"},
	{"dut.core.i_data", "i_data", "IF"},
	{"dut.core.fd_valid", "fd_valid", "ID"},
	{"dut.core.fd_pc", "fd_pc", "ID"},
	{"dut.core.fd_inst", "fd_inst", "ID"},
	{"dut.core.rs1", "rs1", "ID"},
	{"dut.core.rs2", "rs2", "ID"},
	{"dut.core.rd", "rd", "ID"},
	{"dut.core.imm", "imm", "ID"},
	{"dut.core.id_rs1_val", "id_rs1_val", "ID"},
	{"dut.core.id_rs2_val", "id_rs2_val", "ID"},
	{"dut.core.load_use", "load_use", "hazard"},
	{"dut.core.ecall_drain", "ecall_drain", "hazard"},
	{"dut.core.id_stall", "id_stall", "hazard"},
	{"dut.core.ex_stall", "ex_stall", "hazard"},
	{"dut.core.mem_stall", "mem_stall", "hazard"},
	{"dut.core.front_stall", "front_stall", "hazard"},
	{"dut.core.redirect", "redirect", "hazard"},
	{"dut.core.redirect_pc", "redirect_pc", "hazard"},
	{"dut.core.de_valid", "de_valid", "EX"},
	{"dut.core.de_pc", "de_pc", "EX"},
	{"dut.core.de_inst", "de_inst", "EX"},
	{"dut.core.de_rs1", "de_rs1", "EX"},
	{"dut.core.de_rs2", "de_rs2", "EX"},
	{"dut.core.de_rd", "de_rd", "EX"},
	{"dut.core.fwd_a", "fwd_a", "EX"},
	{"dut.core.fwd_b", "fwd_b", "EX"},
	{"dut.core.ex_rs1", "ex_rs1", "EX"},
	{"dut.core.ex_rs2", "ex_rs2", "EX"},
	{"dut.core.alu_a", "alu_a", "EX"},
	{"dut.core.alu_b", "alu_b", "EX"},
	{"dut.core.alu_y", "alu_y", "EX"},
	{"dut.core.br_taken", "br_taken", "EX"},
	{"dut.core.ex_result", "ex_result", "EX"},
	{"dut.core.em_valid", "em_valid", "MEM"},
	{"dut.core.em_pc", "em_pc", "MEM"},
	{"dut.core.em_inst", "em_inst", "MEM"},
	{"dut.core.em_rd", "em_rd", "MEM"},
	{"dut.core.em_result", "em_result", "MEM"},
	{"dut.core.d_valid", "d_valid", "MEM"},
	{"dut.core.d_we", "d_we", "MEM"},
	{"dut.core.d_addr", "d_addr", "MEM"},
	{"dut.core.d_ready", "d_ready", "MEM"},
	{"dut.core.load_val", "load_val", "MEM"},
	{"dut.core.mem_val", "mem_val", "MEM"},
	{"dut.u_ram.cnt", "ram_cnt", "MEM"},
	{"dut.tx_valid", "tx_valid", "MEM"},
	{"dut.tx_data", "tx_data", "MEM"},
	{"dut.core.mw_valid", "mw_valid", "WB"},
	{"dut.core.mw_pc", "mw_pc", "WB"},
	{"dut.core.mw_inst", "mw_inst", "WB"},
	{"dut.core.mw_rd", "mw_rd", "WB"},
	{"dut.core.mw_val", "mw_val", "WB"},
	{"dut.core.rf_we", "rf_we", "WB"},
	{"dut.core.rf_rd", "rf_rd", "WB"},
	{"dut.core.rf_wdata", "rf_wdata", "WB"},
	{"dut.core.commit", "commit", "WB"},
}

// 照合用 (tb.v が引き出しているレジスタ)。JSON には出さない
var checkRegs = []struct {
	path string
	n    int
}{{"tb.x_ra", 1}, {"tb.x_sp", 2}, {"tb.x_a0", 10}, {"tb.x_a1", 11}, {"tb.x_a2", 12}, {"tb.x_a7", 17}}

// ListingLine はソースの命令 1 つ。
type ListingLine struct {
	Addr string `json:"a"`
	Word string `json:"w,omitempty"`
	Line int    `json:"l"`
	Dis  string `json:"d,omitempty"` // 逆アセンブル (擬似命令の展開後)
	Kind string `json:"k"`           // "inst" / "data"
}

// PipeInst はパイプラインを流れた命令 1 つ (動的な実行インスタンス)。
type PipeInst struct {
	PC      string `json:"pc,omitempty"`
	Word    string `json:"w,omitempty"`
	Dis     string `json:"d,omitempty"`
	Bubble  string `json:"b,omitempty"` // バブルなら原因 ("load-use" / "ecall" / "bus")
	Flushed int    `json:"f,omitempty"` // 捨てられたサイクル + 1 (0 = 捨てられていない)
	Commit  int    `json:"c,omitempty"` // WB を抜けたサイクル + 1
}

// Pipe はパイプライン版だけの解析結果。
type Pipe struct {
	Insts  []PipeInst `json:"insts"`
	Stages [][5]int   `json:"st"`  // サイクルごとの IF ID EX MEM WB にいる命令 (Insts の番号, -1 = 空)
	Fwd    [][2]int   `json:"fwd"` // サイクルごと: EX の rs1 / rs2 の出どころ (0 = ID で読んだ値, 1 = MEM (EX/MEM), 2 = WB (MEM/WB), -1 = 使わない)
}

// Trace は 1 本のトレース。
type Trace struct {
	Demo      string            `json:"demo"`
	Core      string            `json:"core"`
	Title     string            `json:"title"`
	Wait      int               `json:"wait"`
	File      string            `json:"file"`
	Source    string            `json:"source"`
	Listing   []ListingLine     `json:"listing"`
	Cycles    int               `json:"cycles"`
	Instret   int               `json:"instret"`
	Exit      *int              `json:"exit"` // 打ち切ったら null
	Truncated bool              `json:"truncated"`
	Console   string            `json:"console"`
	Sigs      []Sig             `json:"sigs"`
	V         [][]string        `json:"v"`    // [サイクル][信号] 16 進 (先頭の 0 は省略), 1 ビットは "0"/"1"
	Regs      [][32]string      `json:"regs"` // [サイクル] そのサイクルの終わりのエッジの後のレジスタ
	Init      [32]string        `json:"init"` // 最初のサイクルの前 (リセット直後) のレジスタ
	Dis       map[string]string `json:"dis"`  // pc (16 進) → 逆アセンブル
	Pipe      *Pipe             `json:"pipe,omitempty"`
}

func main() {
	out := flag.String("o", "", "出力ディレクトリ (必須)")
	only := flag.String("demo", "", "このデモだけ (省略時は全部)")
	core := flag.String("core", "", "single / pipe (省略時は両方)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: trace2json -o DIR [-demo NAME] [-core single|pipe]")
		os.Exit(2)
	}
	check(os.MkdirAll(*out, 0o755))
	work, err := os.MkdirTemp("", "trace2json")
	check(err)
	defer os.RemoveAll(work)

	cores := []string{"single", "pipe"}
	if *core != "" {
		cores = []string{*core}
	}
	vvps := map[string]string{}
	for _, c := range cores {
		vvps[c] = filepath.Join(work, c+".vvp")
		check(sim.Compile(c, vvps[c]))
	}

	type entry struct {
		Demo
		Cycles  map[string]int `json:"cycles"`
		Instret map[string]int `json:"instret"`
	}
	var index []entry
	total := 0
	for _, d := range Demos {
		if *only != "" && d.Name != *only {
			continue
		}
		e := entry{Demo: d, Cycles: map[string]int{}, Instret: map[string]int{}}
		for _, c := range cores {
			t, err := Run(d, c, vvps[c], work)
			if err != nil {
				fmt.Fprintf(os.Stderr, "trace2json: %s (%s): %v\n", d.Name, c, err)
				os.Exit(1)
			}
			b, err := json.Marshal(t)
			check(err)
			name := filepath.Join(*out, d.Name+"-"+c+".json")
			check(os.WriteFile(name, b, 0o644))
			total += len(b)
			e.Cycles[c], e.Instret[c] = t.Cycles, t.Instret
			fmt.Fprintf(os.Stderr, "  %-10s %-6s %4d cycles %4d instret %6.1f KB\n", d.Name, c, t.Cycles, t.Instret, float64(len(b))/1024)
		}
		index = append(index, e)
	}
	b, err := json.MarshalIndent(index, "", " ")
	check(err)
	check(os.WriteFile(filepath.Join(*out, "index.json"), b, 0o644))
	fmt.Fprintf(os.Stderr, "trace2json: %d demos, %.1f KB total → %s\n", len(index), float64(total)/1024, *out)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "trace2json:", err)
		os.Exit(1)
	}
}

var summaryRE = regexp.MustCompile(`\[hw\] exit=(-?\d+) cycles=(\d+) instret=(\d+)`)

// Run は 1 本のデモを 1 つのコアでシミュレーションしてトレースを作る。
func Run(d Demo, core, vvp, work string) (*Trace, error) {
	path := filepath.Join(sim.Dir(), d.File)
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	prog, err := rvasm.BuildProgram(rvasm.Source{Name: filepath.Base(path), Text: string(src)})
	if err != nil {
		return nil, err
	}
	im, err := loader.Load(prog.ELF, []string{"prog"})
	if err != nil {
		return nil, err
	}
	base := filepath.Join(work, d.Name+"-"+core)
	if err := im.WriteHex(base + ".hex"); err != nil {
		return nil, err
	}
	args := append([]string{"-n", vvp, "+hex=" + base + ".hex", fmt.Sprintf("+wait=%d", d.Wait),
		"+vcd=" + base + ".vcd", fmt.Sprintf("+wave_len=%d", MaxCycles)}, im.PlusArgs()...)
	cmd := exec.Command("vvp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("vvp: %v\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "ERROR") {
		return nil, fmt.Errorf("simulation error:\n%s", stderr.String())
	}

	t := &Trace{Demo: d.Name, Core: core, Title: d.Title, Wait: d.Wait, File: d.File, Source: string(src), Dis: map[string]string{}}
	if m := summaryRE.FindStringSubmatch(stderr.String()); m != nil {
		code, _ := strconv.Atoi(m[1])
		t.Exit = &code
	} else {
		t.Truncated = true
	}

	for _, l := range prog.Listing {
		ll := ListingLine{Addr: strconv.FormatUint(l.Addr, 16), Line: l.Line, Kind: l.Kind}
		if l.Kind == "inst" {
			ll.Word = fmt.Sprintf("%08x", l.Word)
			ll.Dis = rv.DisasmInst(rv.Decode(l.Word), l.Addr)
			t.Dis[ll.Addr] = ll.Dis
		}
		t.Listing = append(t.Listing, ll)
	}

	specs := singleSigs
	if core == "pipe" {
		specs = pipeSigs
	}
	want := make([]string, 0, len(specs)+len(checkRegs))
	for _, s := range specs {
		want = append(want, s.path)
	}
	for _, c := range checkRegs {
		want = append(want, c.path)
	}
	f, err := os.Open(base + ".vcd")
	if err != nil {
		return nil, err
	}
	tab, err := vcd.Sample(f, "tb.clk", want)
	f.Close()
	if err != nil {
		return nil, err
	}
	n := len(specs)
	for i, s := range specs {
		t.Sigs = append(t.Sigs, Sig{N: s.name, W: tab.Signals[i].Width, G: s.group})
	}
	col := map[string]int{}
	for i, s := range specs {
		col[s.name] = i
	}
	t.Cycles = len(tab.Rows)
	u := func(row []string, name string) uint64 {
		v, _ := vcd.Uint(row[col[name]])
		return v
	}

	// ---- 値・レジスタファイル・コンソール ----
	var regs [32]uint64
	regs[2] = im.SP
	for k, r := range regs {
		t.Init[k] = strconv.FormatUint(r, 16)
	}
	var console strings.Builder
	for i, row := range tab.Rows {
		vals := make([]string, n)
		for j := 0; j < n; j++ {
			vals[j] = vcd.HexTrim(row[j])
		}
		t.V = append(t.V, vals)
		// 照合: このサイクルの値は、前のエッジまでの書き込みを反映したレジスタと一致するはず
		for k, c := range checkRegs {
			if v, ok := vcd.Uint(row[n+k]); ok && v != regs[c.n] {
				return nil, fmt.Errorf("cycle %d: x%d: VCD %x, reconstructed %x", i, c.n, v, regs[c.n])
			}
		}
		if u(row, "commit") == 1 {
			t.Instret++
		}
		if u(row, "tx_valid") == 1 {
			console.WriteByte(byte(u(row, "tx_data")))
		}
		if rd := u(row, "rf_rd"); u(row, "rf_we") == 1 && rd != 0 {
			regs[rd] = u(row, "rf_wdata")
		}
		var rs [32]string
		for k, r := range regs {
			rs[k] = strconv.FormatUint(r, 16)
		}
		t.Regs = append(t.Regs, rs)
		if core == "single" {
			pc := u(row, "pc")
			key := strconv.FormatUint(pc, 16)
			if _, ok := t.Dis[key]; !ok {
				t.Dis[key] = rv.DisasmInst(rv.Decode(uint32(u(row, "inst"))), pc)
			}
		}
	}
	t.Console = console.String()

	if core == "pipe" {
		p, err := trackPipe(tab.Rows, func(row []string, name string) uint64 { return u(row, name) })
		if err != nil {
			return nil, err
		}
		for k := range p.Insts {
			in := &p.Insts[k]
			if in.Bubble != "" {
				continue
			}
			pc, _ := strconv.ParseUint(in.PC, 16, 64)
			w, _ := strconv.ParseUint(in.Word, 16, 32)
			in.Dis = rv.DisasmInst(rv.Decode(uint32(w)), pc)
		}
		t.Pipe = p
	}
	return t, nil
}

// trackPipe は各サイクルの「どのステージにどの命令がいるか」を、パイプラインレジスタの
// valid とストール・フラッシュ信号から core_pipe.v と同じ規則で追いかける。
// (pc の値だけでは、ループで同じ命令が何度も流れるのを区別できないので)
func trackPipe(rows [][]string, u func([]string, string) uint64) (*Pipe, error) {
	p := &Pipe{}
	const IF, ID, EX, MEM, WB = 0, 1, 2, 3, 4
	newInst := func(in PipeInst) int {
		p.Insts = append(p.Insts, in)
		return len(p.Insts) - 1
	}
	hex := func(v uint64) string { return strconv.FormatUint(v, 16) }
	isBubble := func(id int) bool { return id >= 0 && p.Insts[id].Bubble != "" }
	var prev [5]int
	var prow []string
	for t, row := range rows {
		var st [5]int
		for k := range st {
			st[k] = -1
		}
		pc := u(row, "pc")
		if t == 0 {
			st[IF] = newInst(PipeInst{PC: hex(pc), Word: fmt.Sprintf("%08x", u(row, "i_data"))})
			// リセット直後は後ろのステージは空
		} else {
			redirect := u(prow, "redirect") == 1
			memStall := u(prow, "mem_stall") == 1
			exStall := u(prow, "ex_stall") == 1
			idStall := u(prow, "id_stall") == 1
			front := memStall || exStall || idStall
			// IF
			if !redirect && front {
				st[IF] = prev[IF]
			} else {
				st[IF] = newInst(PipeInst{PC: hex(pc), Word: fmt.Sprintf("%08x", u(row, "i_data"))})
			}
			// ID
			if u(row, "fd_valid") == 1 {
				if front {
					st[ID] = prev[ID]
				} else {
					st[ID] = prev[IF]
				}
			}
			// EX
			if u(row, "de_valid") == 1 {
				if memStall || exStall {
					st[EX] = prev[EX]
				} else {
					st[EX] = prev[ID]
				}
			} else if !redirect {
				if memStall || exStall {
					if isBubble(prev[EX]) {
						st[EX] = prev[EX]
					}
				} else if idStall && prev[ID] >= 0 {
					cause := "load-use"
					if u(prow, "load_use") == 0 {
						cause = "ecall"
					}
					st[EX] = newInst(PipeInst{Bubble: cause})
				}
			}
			// MEM
			if u(row, "em_valid") == 1 {
				if memStall {
					st[MEM] = prev[MEM]
				} else {
					st[MEM] = prev[EX]
				}
			} else if memStall {
				if isBubble(prev[MEM]) {
					st[MEM] = prev[MEM]
				}
			} else if exStall {
				st[MEM] = newInst(PipeInst{Bubble: "ecall"})
			} else if isBubble(prev[EX]) {
				st[MEM] = prev[EX]
			}
			// WB
			if u(row, "mw_valid") == 1 {
				st[WB] = prev[MEM]
			} else if memStall {
				st[WB] = newInst(PipeInst{Bubble: "bus"})
			} else if isBubble(prev[MEM]) {
				st[WB] = prev[MEM]
			}
		}
		// 照合: 追いかけた命令の pc が各パイプラインレジスタの pc と一致するか
		for k, name := range []string{"", "fd_pc", "de_pc", "em_pc", "mw_pc"} {
			id := st[k]
			if id < 0 || isBubble(id) {
				continue
			}
			if name != "" && hex(u(row, name)) != p.Insts[id].PC {
				return nil, fmt.Errorf("cycle %d: stage %d: tracked pc %s, %s=%x", t, k, p.Insts[id].PC, name, u(row, name))
			}
		}
		if id := st[ID]; id >= 0 {
			p.Insts[id].Word = fmt.Sprintf("%08x", u(row, "fd_inst"))
		}
		if id := st[WB]; id >= 0 && !isBubble(id) {
			if u(row, "commit") != 1 {
				return nil, fmt.Errorf("cycle %d: instruction in WB but commit=0", t)
			}
			p.Insts[id].Commit = t + 1
		}
		if u(row, "redirect") == 1 {
			for _, k := range []int{IF, ID} {
				if id := st[k]; id >= 0 {
					p.Insts[id].Flushed = t + 1
				}
			}
		}
		// フォワーディング: EX にいる命令が rs1 / rs2 を本当に読むときだけ
		fwd := [2]int{-1, -1}
		if id := st[EX]; id >= 0 && !isBubble(id) && u(row, "de_valid") == 1 {
			w := uint32(u(row, "de_inst"))
			op := w & 0x7f
			usesRs1 := !(op == 0x37 || op == 0x17 || op == 0x6f)
			usesRs2 := op == 0x33 || op == 0x3b || op == 0x23 || op == 0x63
			if usesRs1 {
				fwd[0] = int(u(row, "fwd_a"))
			}
			if usesRs2 {
				fwd[1] = int(u(row, "fwd_b"))
			}
		}
		p.Stages = append(p.Stages, st)
		p.Fwd = append(p.Fwd, fwd)
		prev, prow = st, row
	}
	return p, nil
}
