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
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type sig struct {
	name  string // 階層名 (tb.dut.core.alu_y)
	width int
	val   string // 現在値 (2 進文字列)
}

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

	byID := map[string][]*sig{}
	var all []*sig
	var scope []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var clk *sig
	var rows [][]string
	var want []*sig
	pick := func() {
		for _, w := range flag.Args()[1:] {
			var hit *sig
			for _, s := range all {
				if s.name == w || strings.HasSuffix(s.name, "."+w) {
					if hit == nil || len(s.name) < len(hit.name) {
						hit = s
					}
				}
			}
			if hit == nil {
				fmt.Fprintf(os.Stderr, "vcdcat: signal %q not found\n", w)
				os.Exit(1)
			}
			want = append(want, hit)
		}
		for _, s := range all {
			if s.name == "tb.clk" {
				clk = s
			}
		}
	}
	snapshot := func() {
		row := make([]string, len(want))
		for i, s := range want {
			row[i] = format(s)
		}
		rows = append(rows, row)
	}
	// 同じ時刻の変化はまとめて処理する。その時刻にクロックが 0→1 になるなら、
	// 変化を反映する「前」の値 (= エッジ直前の確定値) を 1 行として記録する
	type change struct{ id, val string }
	var pending []change
	flush := func() {
		for _, c := range pending {
			for _, s := range byID[c.id] {
				if s == clk && c.val == "1" && clk.val == "0" {
					snapshot()
				}
			}
		}
		for _, c := range pending {
			for _, s := range byID[c.id] {
				s.val = c.val
			}
		}
		pending = pending[:0]
	}
	inHeader := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if inHeader {
			fs := strings.Fields(line)
			switch {
			case len(fs) >= 3 && fs[0] == "$scope":
				scope = append(scope, fs[2])
			case len(fs) >= 1 && fs[0] == "$upscope":
				scope = scope[:len(scope)-1]
			case len(fs) >= 5 && fs[0] == "$var":
				w, _ := strconv.Atoi(fs[2])
				s := &sig{name: strings.Join(append(append([]string{}, scope...), fs[4]), "."), width: w, val: "x"}
				byID[fs[3]] = append(byID[fs[3]], s)
				all = append(all, s)
			case len(fs) >= 1 && fs[0] == "$enddefinitions":
				inHeader = false
				pick()
			}
			continue
		}
		if line == "" || line[0] == '$' {
			continue
		}
		if line[0] == '#' {
			flush()
			continue
		}
		var val, id string
		switch line[0] {
		case 'b', 'B':
			sp := strings.IndexByte(line, ' ')
			val, id = line[1:sp], line[sp+1:]
		case 'r', 'R':
			continue
		default:
			val, id = line[:1], line[1:]
		}
		pending = append(pending, change{id, val})
	}
	flush()

	names := make([]string, len(want))
	widths := make([]int, len(want))
	for i, s := range want {
		names[i] = s.name[strings.LastIndexByte(s.name, '.')+1:]
		widths[i] = len(names[i])
		for _, r := range rows {
			widths[i] = max(widths[i], len(r[i]))
		}
	}
	show := make([]bool, len(rows))
	if *pcFlag != "" {
		pcIdx := -1
		for i, s := range want {
			if names[i] == "pc" && s.width == 64 {
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

// format は値を 1 ビットならそのまま、それ以上なら 16 進で表す (x / z を含めばそのまま)。
func format(s *sig) string {
	v := s.val
	if s.width == 1 {
		return v
	}
	if strings.ContainsAny(v, "xXzZ") {
		return v
	}
	n, ok := new(big).SetString(v)
	if !ok {
		return v
	}
	digits := (s.width + 3) / 4
	return fmt.Sprintf("%0*x", digits, n)
}

// big は 128 ビットまでの値を 16 進にするための最小限の型 (math/big を使うほどでもない)。
type big struct{ hi, lo uint64 }

func (b *big) SetString(bin string) (*big, bool) {
	for _, c := range bin {
		if c != '0' && c != '1' {
			return nil, false
		}
		b.hi = b.hi<<1 | b.lo>>63
		b.lo = b.lo<<1 | uint64(c-'0')
	}
	return b, true
}

func (b *big) Format(f fmt.State, verb rune) {
	w, _ := f.Width()
	var s string
	if b.hi != 0 {
		s = fmt.Sprintf("%x%016x", b.hi, b.lo)
	} else {
		s = fmt.Sprintf("%x", b.lo)
	}
	for len(s) < w {
		s = "0" + s
	}
	fmt.Fprint(f, s)
}
