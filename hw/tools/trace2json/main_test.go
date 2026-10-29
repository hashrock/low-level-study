package main

import (
	"path/filepath"
	"testing"

	"rvhw/sim"
)

// 全デモを両方のコアで動かし、web/hw のページが頼りにしている事実を確かめる。
// (Run 自体も、復元したレジスタファイルと tb の x_a0 などの照合、パイプラインの追跡と
// 各ステージの pc の照合をしているので、それも全部通ることになる)
func TestDemos(t *testing.T) {
	dir := t.TempDir()
	traces := map[string]*Trace{}
	for _, core := range []string{"single", "pipe"} {
		vvp := filepath.Join(dir, core+".vvp")
		if err := sim.Compile(core, vvp); err != nil {
			t.Fatal(err)
		}
		for _, d := range Demos {
			tr, err := Run(d, core, vvp, dir)
			if err != nil {
				t.Fatalf("%s/%s: %v", d.Name, core, err)
			}
			if tr.Truncated || tr.Exit == nil {
				t.Errorf("%s/%s: did not exit within %d cycles", d.Name, core, MaxCycles)
			}
			traces[d.Name+"-"+core] = tr
		}
	}
	for _, d := range Demos {
		s, p := traces[d.Name+"-single"], traces[d.Name+"-pipe"]
		if s.Instret != p.Instret || *s.Exit != *p.Exit || s.Console != p.Console {
			t.Errorf("%s: single (%d instret, exit %d, %q) vs pipe (%d, %d, %q)",
				d.Name, s.Instret, *s.Exit, s.Console, p.Instret, *p.Exit, p.Console)
		}
	}

	// README の addi a0, a0, -1 (0xfff50513): alu_a=5, alu_b=-1, alu_y=4、a0 はエッジで 5 → 4
	tr := traces["arith-single"]
	get := func(tr *Trace, c int, name string) string {
		for i, s := range tr.Sigs {
			if s.N == name {
				return tr.V[c][i]
			}
		}
		t.Fatalf("no signal %s", name)
		return ""
	}
	found := false
	for c := range tr.V {
		if get(tr, c, "inst") != "fff50513" {
			continue
		}
		found = true
		want := map[string]string{"alu_a": "5", "alu_b": "ffffffffffffffff", "alu_y": "4", "b_sel": "1",
			"rf_we": "1", "rf_rd": "a", "imm": "ffffffffffffffff", "rs1_val": "5"}
		for k, v := range want {
			if g := get(tr, c, k); g != v {
				t.Errorf("addi: %s = %s, want %s", k, g, v)
			}
		}
		if tr.Regs[c-1][10] != "5" || tr.Regs[c][10] != "4" {
			t.Errorf("addi: a0 before/after edge = %s/%s, want 5/4", tr.Regs[c-1][10], tr.Regs[c][10])
		}
	}
	if !found {
		t.Error("arith: addi a0, a0, -1 not found")
	}

	// RAM 待ち 2: 単一サイクル版でストールが出る
	stalls := 0
	for c := range traces["stall-single"].V {
		if get(traces["stall-single"], c, "stall") == "1" {
			stalls++
		}
	}
	if stalls != 4 { // ld と sd でそれぞれ 2 サイクル
		t.Errorf("stall: %d stall cycles, want 4", stalls)
	}

	// パイプライン: load-use でバブル 1 つ、フラッシュで 2 命令が捨てられる、フォワーディング 1 と 2 が両方出る
	count := func(tr *Trace, f func(PipeInst) bool) int {
		n := 0
		for _, in := range tr.Pipe.Insts {
			if f(in) {
				n++
			}
		}
		return n
	}
	if n := count(traces["loaduse-pipe"], func(in PipeInst) bool { return in.Bubble == "load-use" }); n != 1 {
		t.Errorf("loaduse: %d load-use bubbles, want 1", n)
	}
	if n := count(traces["flush-pipe"], func(in PipeInst) bool { return in.Flushed != 0 }); n != 2 {
		t.Errorf("flush: %d flushed, want 2", n)
	}
	seen := map[int]bool{}
	for _, f := range traces["forward-pipe"].Pipe.Fwd {
		seen[f[0]], seen[f[1]] = true, true
	}
	if !seen[1] || !seen[2] {
		t.Errorf("forward: sources seen %v, want both 1 (EX/MEM) and 2 (MEM/WB)", seen)
	}
	if s := traces["mmio-single"].Console; s != "Hi!\n" {
		t.Errorf("mmio: console %q", s)
	}
}
