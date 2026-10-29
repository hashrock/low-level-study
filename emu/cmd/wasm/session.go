package main

// ブラウザ (web/os) 向けの薄いラッパ。JS からは文字列 (JSON) と素朴な値だけをやりとりする。
// syscall/js に依存しないので、普通の go test で確かめられる (session_test.go)。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"rvemu/rv"
)

type session struct {
	kernel []byte
	ramMiB int
	m      *rv.Machine
	out    bytes.Buffer // UART 出力 (run のたびに取り出す)
}

const traceSize = 64

// load はカーネルを読み込んでマシンを作り直す (reset も同じ)。
func (s *session) load(kernel []byte, ramMiB int) error {
	if ramMiB <= 0 {
		ramMiB = 4
	}
	s.kernel, s.ramMiB = kernel, ramMiB
	return s.reset()
}

func (s *session) reset() error {
	s.out.Reset()
	m, err := rv.NewVirtMachine(s.kernel, &s.out, nil, &rv.VirtOptions{RAMSize: uint64(s.ramMiB) << 20})
	if err != nil {
		return err
	}
	m.EnableTrace(traceSize)
	m.SetRealtimeIdle(true)
	s.m = m
	return nil
}

type runResult struct {
	Steps   uint64 `json:"steps"`   // 実行した命令数
	Out     string `json:"out"`     // この間の UART 出力
	Exited  bool   `json:"exited"`  // 電源断・停止
	Code    int    `json:"code"`    // 終了コード
	Err     string `json:"err"`     // 異常停止の理由
	Idle    bool   `json:"idle"`    // WFI で実時間待ちになった
	Trapped bool   `json:"trapped"` // untilTrap でトラップに当たった
}

// run は最大 max ステップ実行する。idleTicks は WFI で早送りしてよい mtime の量
// (= 経過した実時間 × 10MHz)。untilTrap なら次のトラップで止まる。
func (s *session) run(max uint64, idleTicks uint64, untilTrap bool) string {
	m := s.m
	m.SetIdleBudget(idleTicks)
	before := m.CPU.Steps
	var r runResult
	if untilTrap {
		r.Trapped = m.RunUntilTrap(max)
	} else {
		m.Run(max)
	}
	r.Steps = m.CPU.Steps - before
	r.Idle = m.Idle()
	r.Exited, r.Code = m.CPU.Halted, m.CPU.ExitCode
	if m.CPU.Err != nil {
		r.Err = m.CPU.Err.Error()
	}
	r.Out = s.out.String()
	s.out.Reset()
	return toJSON(r)
}

func (s *session) input(text string) { s.m.SendInput([]byte(text)) }

func hex(v uint64) string { return fmt.Sprintf("%016x", v) }

type stateJSON struct {
	X         []string          `json:"x"`
	PC        string            `json:"pc"`
	Sym       string            `json:"sym"`
	Priv      string            `json:"priv"`
	Instret   uint64            `json:"instret"`
	CSR       map[string]string `json:"csr"`
	Mtime     string            `json:"mtime"`
	Mtimecmp  string            `json:"mtimecmp"`
	Halted    bool              `json:"halted"`
	Code      int               `json:"code"`
	Err       string            `json:"err"`
	Pending   int               `json:"uartPending"`
	TrapCount uint64            `json:"trapCount"`
	Mcause    string            `json:"mcauseName"`
}

func (s *session) state() string {
	m := s.m
	snap := m.Snapshot()
	st := stateJSON{PC: hex(snap.PC), Sym: m.Symbolize(snap.PC), Priv: rv.PrivName(snap.Priv), Instret: snap.Instret,
		CSR: map[string]string{}, Mtime: hex(snap.Mtime), Mtimecmp: hex(snap.Mtimecmp),
		Halted: snap.Halted, Code: snap.ExitCode, Err: snap.Err, Pending: m.UART.Pending(),
		TrapCount: m.TrapCount(), Mcause: rv.CauseName(snap.CSR["mcause"])}
	for _, x := range snap.X {
		st.X = append(st.X, hex(x))
	}
	for k, v := range snap.CSR {
		st.CSR[k] = hex(v)
	}
	return toJSON(st)
}

type lineJSON struct {
	Addr string `json:"addr"`
	Raw  string `json:"raw"`
	Text string `json:"text"`
	Sym  string `json:"sym,omitempty"`
}

// disasm は pc の前後を逆アセンブルする (pc が空なら現在の PC)。
func (s *session) disasm(pcHex string, before, after int) string {
	pc := s.m.CPU.PC
	if pcHex != "" {
		v, err := parseAddr(pcHex)
		if err != nil {
			return toJSON(map[string]string{"error": err.Error()})
		}
		pc = v
	}
	var out []lineJSON
	for _, l := range s.m.DisasmAround(pc, before, after) {
		raw := fmt.Sprintf("%08x", l.Raw)
		if l.Size == 2 {
			raw = fmt.Sprintf("    %04x", l.Raw)
		}
		if !l.OK {
			raw = "????????"
		}
		out = append(out, lineJSON{Addr: hex(l.Addr), Raw: raw, Text: l.Text, Sym: l.Symbol})
	}
	return toJSON(out)
}

type traceJSON struct {
	Seq  uint64 `json:"seq"`
	PC   string `json:"pc"`
	Priv string `json:"priv"`
	Text string `json:"text"`
}

func (s *session) trace() string {
	var out []traceJSON
	for _, t := range s.m.Trace() {
		out = append(out, traceJSON{t.Seq, hex(t.PC), rv.PrivName(t.Priv), t.Text})
	}
	return toJSON(out)
}

type trapJSON struct {
	Seq       uint64 `json:"seq"`
	Kind      string `json:"kind"`
	Cause     string `json:"cause"`
	CauseName string `json:"causeName"`
	From      string `json:"from"`
	To        string `json:"to"`
	PC        string `json:"pc"`
	PCSym     string `json:"pcSym"`
	Target    string `json:"target"`
	TargetSym string `json:"targetSym"`
	A7        uint64 `json:"a7"`
	Ecall     bool   `json:"ecall"`
}

// traps は直近 n 件のトラップを新しい順に返す。
func (s *session) traps(n int) string {
	evs := s.m.Traps()
	if len(evs) > n {
		evs = evs[len(evs)-n:]
	}
	out := []trapJSON{}
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		t := trapJSON{Seq: e.Seq, Kind: e.Kind, From: rv.PrivName(e.From), To: rv.PrivName(e.To),
			PC: hex(e.PC), PCSym: s.m.Symbolize(e.PC), Target: hex(e.Target), TargetSym: s.m.Symbolize(e.Target), A7: e.A7}
		if e.Kind == "exception" || e.Kind == "interrupt" {
			t.Cause = hex(e.Cause)
			t.CauseName = rv.CauseName(e.Cause)
			t.Ecall = e.Kind == "exception" && e.Cause >= 8 && e.Cause <= 11
		}
		out = append(out, t)
	}
	return toJSON(out)
}

// mem は addr から n バイトを 16 進文字列で返す (読めない部分は省かれる)。
func (s *session) mem(addrHex string, n int) string {
	addr, err := parseAddr(addrHex)
	if err != nil {
		return toJSON(map[string]string{"error": err.Error()})
	}
	b := s.m.ReadMem(addr, uint64(n))
	return toJSON(map[string]string{"addr": hex(addr), "hex": fmt.Sprintf("%x", b)})
}

// symbol は名前からアドレスを引く (無ければ空文字列)。
func (s *session) symbol(name string) string {
	if sym, ok := s.m.Symbols.Find(name); ok {
		return hex(sym.Addr)
	}
	return ""
}

func parseAddr(s string) (uint64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", ""))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("アドレスは 16 進数で入力してください: %q", s)
	}
	return v, nil
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":` + strconv.Quote(err.Error()) + `}`
	}
	return string(b)
}
