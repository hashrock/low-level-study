package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// ブラウザと同じ手順 (チャンク実行 + 実時間アイドル + キー入力) でシェルまで動かす。
func TestSessionBootsZigOS(t *testing.T) {
	kernel, err := os.ReadFile("../../../os/zig-out/bin/kernel.elf")
	if err != nil {
		t.Skip("zigos kernel not built")
	}
	s := &session{}
	if err := s.load(kernel, 4); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	runUntil := func(want string) {
		for i := 0; i < 2000 && !strings.Contains(out.String(), want); i++ {
			var r runResult
			json.Unmarshal([]byte(s.run(200_000, 160_000, false)), &r)
			out.WriteString(r.Out)
			if r.Exited {
				t.Fatalf("exited: %+v", r)
			}
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("no %q in %q", want, out.String())
		}
	}
	runUntil("zigos:/$ ")
	s.input("ls\r")
	runUntil("README")
	s.input("uptime\r")
	runUntil("ticks)")

	var st stateJSON
	if err := json.Unmarshal([]byte(s.state()), &st); err != nil || len(st.X) != 32 || st.CSR["mtvec"] == "" {
		t.Fatalf("state: %v %+v", err, st)
	}
	var lines []lineJSON
	json.Unmarshal([]byte(s.disasm("", 8, 8)), &lines)
	if len(lines) != 17 || lines[8].Addr != st.PC {
		t.Fatalf("disasm around %s: %+v", st.PC, lines)
	}
	var traps []trapJSON
	json.Unmarshal([]byte(s.traps(500)), &traps)
	ecalls := 0
	for _, tr := range traps {
		if tr.Ecall && tr.From == "U" && tr.To == "M" {
			ecalls++
		}
	}
	if ecalls == 0 {
		t.Errorf("no ecall U→M in %d traps", len(traps))
	}
	var mem map[string]string
	json.Unmarshal([]byte(s.mem("0x80000000", 16)), &mem)
	if len(mem["hex"]) != 32 {
		t.Errorf("mem: %v", mem)
	}
	if s.symbol("kmain") == "" {
		t.Error("no kmain symbol")
	}
	// アイドル中はトラップが起きない (カーネルの wfi ループでタイマを処理する) ので、キーを送ってから
	s.input("e")
	var r runResult
	json.Unmarshal([]byte(s.run(1_000_000, 1_000_000, true)), &r)
	if !r.Trapped {
		t.Errorf("untilTrap: %+v", r)
	}
}
