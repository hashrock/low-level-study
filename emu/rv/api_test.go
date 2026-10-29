package rv

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Machine の観察用 API (Snapshot / トラップログ / トレース / 逆アセンブル / 入力注入) のテスト。
func TestMachineAPI(t *testing.T) {
	var out bytes.Buffer
	m, err := NewVirtMachine(miniKernel(), &out, nil, &VirtOptions{RAMSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	m.EnableTrace(8)
	// UART 入力待ちのループで止まる (入力がまだ無い)
	m.Run(10_000)
	if out.String() != "OK\n" || m.Exited() {
		t.Fatalf("out=%q exited=%v", out.String(), m.Exited())
	}
	// メモリビューアで UART の LSR を読んでも副作用がない
	m.SendInput([]byte("A"))
	if b := m.ReadMem(UARTBase, 8); len(b) != 8 || b[0] != 'A' || b[5]&1 == 0 {
		t.Fatalf("uart peek = %x", b)
	}
	if m.UART.Pending() != 1 {
		t.Fatal("peek consumed input")
	}
	// mret まで実行すると M→U の記録が残る
	if !m.RunUntilTrap(10_000) {
		t.Fatal("no trap")
	}
	ev := m.Traps()[len(m.Traps())-1]
	if ev.Kind != "mret" || ev.From != PrivM || ev.To != PrivU {
		t.Fatalf("event = %+v", ev)
	}
	s := m.Snapshot()
	if s.Priv != PrivU || s.PC != ev.Target || s.CSR["mie"] != 1<<7 {
		t.Fatalf("snapshot = %+v", s)
	}
	// 次のトラップはタイマ割り込み (U→M)
	if !m.RunUntilTrap(10_000) {
		t.Fatal("no timer")
	}
	ev = m.Traps()[len(m.Traps())-1]
	if ev.Kind != "interrupt" || CauseName(ev.Cause) != "machine timer interrupt" || ev.From != PrivU || ev.To != PrivM {
		t.Fatalf("event = %+v", ev)
	}
	tr := m.Trace()
	if len(tr) != 8 || tr[7].Priv != PrivU || !strings.HasPrefix(tr[7].Text, "jal") {
		t.Fatalf("trace = %+v", tr)
	}
	d := m.DisasmAround(m.CPU.PC, 3, 3)
	if len(d) != 7 || d[3].Addr != m.CPU.PC || !strings.HasPrefix(d[3].Text, "csrrs") {
		t.Fatalf("disasm = %+v", d)
	}
	exited, code, _ := m.Run(0)
	if !exited || code != 0 || out.String() != "OK\nB!" {
		t.Fatalf("exited=%v code=%d out=%q", exited, code, out.String())
	}
}

// リアルタイムアイドル: WFI で待つとき、与えた時間だけ mtime が進み、Run は途中で戻る。
func TestRealtimeIdle(t *testing.T) {
	kernel, err := os.ReadFile("../../os/zig-out/bin/kernel.elf")
	if err != nil {
		t.Skip("zigos kernel not built")
	}
	var out bytes.Buffer
	m, err := NewVirtMachine(kernel, &out, nil, &VirtOptions{RAMSize: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	m.SetRealtimeIdle(true)
	for i := 0; i < 1000 && !strings.Contains(out.String(), "$ "); i++ {
		m.SetIdleBudget(200_000)
		m.Run(5_000_000)
	}
	if !strings.Contains(out.String(), "zigos:/$ ") {
		t.Fatalf("no prompt: %q", out.String())
	}
	t0 := m.CLINT.Mtime
	m.Run(5_000_000)
	if !m.Idle() || m.CLINT.Mtime-t0 > 5_000_000 {
		t.Fatalf("idle=%v mtime advanced %d", m.Idle(), m.CLINT.Mtime-t0)
	}
	m.SendInput([]byte("uptime\r"))
	for i := 0; i < 100 && !strings.Contains(out.String(), "ticks"); i++ {
		m.SetIdleBudget(200_000)
		m.Run(5_000_000)
	}
	if _, _, ok := strings.Cut(out.String(), "uptime"); !ok {
		t.Fatalf("out=%q", out.String())
	}
	// ecall の記録 (a7 = システムコール番号) がある
	found := false
	for _, ev := range m.Traps() {
		if ev.Kind == "exception" && ev.Cause == excEcallU {
			found = true
		}
	}
	if !found {
		t.Error("no ecall in trap log")
	}
	t.Logf("%q", out.String())
}
