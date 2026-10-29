// Package vcd は VCD 波形ファイル (Value Change Dump) を読み、
// 「クロックの立ち上がりエッジ直前の値」を 1 サイクル 1 行の表にする。
//
// tools/vcdcat (端末で表示) と tools/trace2json (web/hw 用の JSON) が使う。
//
// 信号名は階層名 (tb.dut.core.alu_y) か、その末尾 ("alu_y", "u_dec.alu_op") で指定する。
// 末尾が一致するものが複数あれば、いちばん短い (浅い) ものを選ぶ。
package vcd

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Signal は選んだ信号 1 本。
type Signal struct {
	Name  string // 階層名 (tb.dut.core.alu_y)
	Width int
}

// Table は Sample の結果。Rows[i][j] はサイクル i の信号 j の値 (2 進文字列。x / z を含むこともある)。
type Table struct {
	Signals []Signal
	Rows    [][]string
}

type sig struct {
	Signal
	val string
}

// Sample は VCD を読み、clock (例 "tb.clk") が 0→1 になる時刻ごとに、
// その時刻の変化を反映する「前」の値 (= そのサイクルで落ち着いた値) を 1 行として記録する。
func Sample(r io.Reader, clock string, want []string) (*Table, error) {
	byID := map[string][]*sig{}
	var all []*sig
	var scope []string
	var clk *sig
	var picked []*sig
	t := &Table{}

	pick := func() error {
		for _, w := range want {
			var hit *sig
			for _, s := range all {
				if s.Name == w || strings.HasSuffix(s.Name, "."+w) {
					if hit == nil || len(s.Name) < len(hit.Name) {
						hit = s
					}
				}
			}
			if hit == nil {
				return fmt.Errorf("vcd: signal %q not found", w)
			}
			picked = append(picked, hit)
			t.Signals = append(t.Signals, hit.Signal)
		}
		for _, s := range all {
			if s.Name == clock {
				clk = s
			}
		}
		if clk == nil {
			return fmt.Errorf("vcd: clock %q not found", clock)
		}
		return nil
	}
	snapshot := func() {
		row := make([]string, len(picked))
		for i, s := range picked {
			row[i] = s.val
		}
		t.Rows = append(t.Rows, row)
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

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
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
				s := &sig{Signal: Signal{Name: strings.Join(append(append([]string{}, scope...), fs[4]), "."), Width: w}, val: "x"}
				byID[fs[3]] = append(byID[fs[3]], s)
				all = append(all, s)
			case len(fs) >= 1 && fs[0] == "$enddefinitions":
				inHeader = false
				if err := pick(); err != nil {
					return nil, err
				}
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
			if sp < 0 {
				continue
			}
			val, id = line[1:sp], line[sp+1:]
		case 'r', 'R':
			continue
		default:
			val, id = line[:1], line[1:]
		}
		pending = append(pending, change{id, val})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if inHeader {
		return nil, fmt.Errorf("vcd: no $enddefinitions")
	}
	flush()
	return t, nil
}

// Hex は 2 進文字列の値を、幅に合わせて 0 埋めした 16 進にする (1 ビットならそのまま、x / z を含めばそのまま)。
func Hex(width int, v string) string {
	if width == 1 || strings.ContainsAny(v, "xXzZ") {
		return v
	}
	s := trimHex(v)
	digits := (width + 3) / 4
	for len(s) < digits {
		s = "0" + s
	}
	return s
}

// HexTrim は Hex と同じだが先頭の 0 を省く ("0" は残す)。JSON を小さくするため。
func HexTrim(v string) string {
	if strings.ContainsAny(v, "xXzZ") {
		return "x"
	}
	return trimHex(v)
}

// trimHex は 2 進文字列 (VCD では上位の 0 が省略されていることがある) を先頭 0 なしの 16 進にする。
func trimHex(bin string) string {
	// 4 ビットずつ下から
	var out []byte
	for i := len(bin); i > 0; i -= 4 {
		j := max(0, i-4)
		n := 0
		for _, c := range bin[j:i] {
			n = n<<1 | int(c-'0')
		}
		out = append(out, "0123456789abcdef"[n])
	}
	for len(out) > 1 && out[len(out)-1] == '0' {
		out = out[:len(out)-1]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) == 0 {
		return "0"
	}
	return string(out)
}

// Uint は 2 進文字列を数値にする (64 ビットまで。x / z を含めば ok = false)。
func Uint(v string) (uint64, bool) {
	if v == "" || strings.ContainsAny(v, "xXzZ") {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 2, 64)
	return n, err == nil
}
