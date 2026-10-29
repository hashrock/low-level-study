// vcdcat: VCD 波形ファイルを「1 クロック 1 行」の表にして端末に出す (GUI が無くても波形を読める)。
//
//	go run ./tools/vcdcat build/wave.vcd pc inst u_dec.alu_op alu_a alu_b alu_y rf_we
//	go run ./tools/vcdcat -pc 10110 -before 1 -after 1 build/wave.vcd pc inst alu_y
//
// 信号名は階層名の末尾で指定する (例: "alu_y" は tb.dut.core.alu_y にマッチ)。
// 値は各クロックの立ち上がりエッジの直前 (= そのサイクルで確定した値) を表示する。
// -pc を付けると、pc がその値のサイクルの前後だけを表示する。
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"rvhw/vcd"
)

func main() {
	pcFlag := flag.String("pc", "", "この pc (16 進) のサイクルの前後だけ表示")
	before := flag.Int("before", 1, "-pc の前に表示するサイクル数")
	after := flag.Int("after", 1, "-pc の後に表示するサイクル数")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: vcdcat [-pc HEX] file.vcd signal...")
		os.Exit(2)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	// 解析本体は rvhw/vcd (tools/trace2json と共用)
	t, err := vcd.Sample(f, "tb.clk", flag.Args()[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, strings.Replace(err.Error(), "vcd:", "vcdcat:", 1))
		os.Exit(1)
	}
	want := t.Signals
	rows := make([][]string, len(t.Rows))
	for i, r := range t.Rows {
		rows[i] = make([]string, len(r))
		for j, v := range r {
			rows[i][j] = vcd.Hex(want[j].Width, v)
		}
	}

	names := make([]string, len(want))
	widths := make([]int, len(want))
	for i, s := range want {
		names[i] = s.Name[strings.LastIndexByte(s.Name, '.')+1:]
		widths[i] = len(names[i])
		for _, r := range rows {
			widths[i] = max(widths[i], len(r[i]))
		}
	}
	show := make([]bool, len(rows))
	if *pcFlag != "" {
		pcIdx := -1
		for i, s := range want {
			if names[i] == "pc" && s.Width == 64 {
				pcIdx = i
			}
		}
		if pcIdx < 0 {
			fmt.Fprintln(os.Stderr, "vcdcat: -pc を使うときは信号に pc を含める")
			os.Exit(1)
		}
		target, _ := strconv.ParseUint(strings.TrimPrefix(*pcFlag, "0x"), 16, 64)
		for i, r := range rows {
			if v, err := strconv.ParseUint(r[pcIdx], 16, 64); err == nil && v == target {
				for j := max(0, i-*before); j <= min(len(rows)-1, i+*after); j++ {
					show[j] = true
				}
			}
		}
	} else {
		for i := range show {
			show[i] = true
		}
	}
	line := func(cells []string) {
		for i, c := range cells {
			fmt.Printf("%-*s ", widths[i], c)
		}
		fmt.Println()
	}
	line(names)
	prev := -1
	for i, r := range rows {
		if !show[i] {
			continue
		}
		if prev >= 0 && i != prev+1 {
			fmt.Println("...")
		}
		line(r)
		prev = i
	}
}
