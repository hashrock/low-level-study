package rv

import "fmt"

// 外部 (ブラウザ UI・他のモジュール) から Machine を観察・操作するための API。

// RegNames は x0..x31 の ABI 名。
var RegNames = regNames

// Snapshot はある時点の CPU の状態。
type Snapshot struct {
	X        [32]uint64
	PC       uint64
	Priv     uint64            // PrivU / PrivS / PrivM
	Instret  uint64            // 実行した命令数
	CSR      map[string]uint64 // 主要な CSR (名前 → 値)
	Mtime    uint64            // CLINT (システムモードのみ)
	Mtimecmp uint64
	Halted   bool
	ExitCode int
	Err      string
}

// SnapshotCSRs は Snapshot に含める CSR の一覧 (表示順)。
var SnapshotCSRs = []string{
	"mstatus", "mtvec", "mepc", "mcause", "mtval", "mie", "mip", "mscratch",
	"medeleg", "mideleg", "satp", "sepc", "scause", "stvec", "mhartid", "misa",
}

var csrByName = func() map[string]uint32 {
	m := map[string]uint32{}
	for n, s := range csrNames {
		m[s] = n
	}
	return m
}()

// ReadCSR は CSR を名前 ("mstatus" など) または番号で読む (副作用なし)。
func (c *CPU) ReadCSR(n uint32) uint64 { return c.readCSR(n) }

// CSRByName は CSR 名から番号を引く。
func CSRByName(name string) (uint32, bool) {
	n, ok := csrByName[name]
	return n, ok
}

// Snapshot は現在のレジスタ・PC・特権レベル・主要 CSR・命令数を返す。
func (m *Machine) Snapshot() Snapshot {
	c := m.CPU
	s := Snapshot{X: c.X, PC: c.PC, Priv: c.Priv, Instret: c.Steps, CSR: map[string]uint64{},
		Halted: c.Halted, ExitCode: c.ExitCode}
	for _, name := range SnapshotCSRs {
		s.CSR[name] = c.readCSR(csrByName[name])
	}
	if m.CLINT != nil {
		s.Mtime, s.Mtimecmp = m.CLINT.Mtime, m.CLINT.Mtimecmp
	}
	if c.Err != nil {
		s.Err = c.Err.Error()
	}
	return s
}

// ReadMem は物理アドレス addr から n バイトを読む。RAM 以外 (デバイス) も読めるが、
// 副作用は起こさない (UART の受信データを消費しない)。読めないバイトは ok=false の位置で切れる。
func (m *Machine) ReadMem(addr, n uint64) []byte {
	if b, ok := m.Bus.ReadBytes(addr, n); ok && b != nil {
		return append([]byte(nil), b...)
	}
	out := make([]byte, 0, n)
	for i := uint64(0); i < n; i++ {
		v, ok := m.Bus.Peek(addr + i)
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out
}

// WriteMem は RAM に書き込む (デバイスには書けない)。
func (m *Machine) WriteMem(addr uint64, data []byte) bool { return m.Bus.WriteBytes(addr, data) }

// SendInput は UART の受信データとして data を注入する (キーボード入力)。ユーザモードでは何もしない。
func (m *Machine) SendInput(data []byte) {
	if m.UART != nil {
		m.UART.Push(data)
	}
}

// ---- 逆アセンブル ----

// DisasmLine は逆アセンブル結果の 1 行。
type DisasmLine struct {
	Addr   uint64
	Size   uint64 // 2 (圧縮命令) / 4
	Raw    uint32 // 命令語 (圧縮命令なら 16 ビット)
	Text   string // "addi a0, a0, 1"
	Symbol string // この行がシンボルの先頭ならその名前
	OK     bool   // メモリが読めたか
}

// DecodeAt は addr の命令を 1 つデコードする (圧縮命令は展開後の形)。
func (m *Machine) DecodeAt(addr uint64) (Inst, bool) {
	lo, ok := m.Bus.Load(addr, 2)
	if !ok || !m.isRAM(addr) {
		return Inst{}, false
	}
	if lo&3 != 3 {
		raw, ok := expandCompressed(uint32(lo))
		if !ok {
			return Inst{Op: ILLEGAL, Raw: uint32(lo), Size: 2, Compressed: uint32(lo)}, true
		}
		in := Decode(raw)
		in.Size, in.Compressed = 2, uint32(lo)
		return in, true
	}
	hi, ok := m.Bus.Load(addr+2, 2)
	if !ok {
		return Inst{}, false
	}
	return Decode(uint32(hi<<16 | lo)), true
}

func (m *Machine) isRAM(addr uint64) bool { return m.Bus.ram(addr, 2) != nil }

func (m *Machine) line(addr uint64) DisasmLine {
	in, ok := m.DecodeAt(addr)
	if !ok {
		return DisasmLine{Addr: addr, Size: 2, Text: "??"}
	}
	l := DisasmLine{Addr: addr, Size: in.Size, Raw: in.Raw, Text: disasmBody(in, addr), OK: true}
	if in.Size == 2 {
		l.Raw = in.Compressed
	}
	if sym, off, ok := m.Symbols.Lookup(addr); ok && off == 0 {
		l.Symbol = sym.Name
	}
	return l
}

// Disasm は addr から count 命令を逆アセンブルする。
func (m *Machine) Disasm(addr uint64, count int) []DisasmLine {
	out := make([]DisasmLine, 0, count)
	for i := 0; i < count; i++ {
		l := m.line(addr)
		out = append(out, l)
		addr += l.Size
	}
	return out
}

// DisasmAround は pc の前 before 命令と pc 以降 after 命令を返す (pc の行を含む)。
// 命令長が 2/4 バイト混在なので、前方は「pc にちょうど着地する開始位置」を探して決める
// (pc を含む関数の先頭が分かればそこから読む)。
func (m *Machine) DisasmAround(pc uint64, before, after int) []DisasmLine {
	start := pc
	if sym, off, ok := m.Symbols.Lookup(pc); ok && off <= uint64(before)*4+256 {
		start = sym.Addr
	} else {
		// 遠い位置から 2 バイトずつ近づけて、pc に着地するものを採用する
		for back := uint64(before) * 4; back > 0; back -= 2 {
			if a, n := m.walk(pc-back, pc); a == pc && n >= before {
				start = pc - back
				break
			}
		}
	}
	var prev []DisasmLine
	for a := start; a < pc; {
		l := m.line(a)
		prev = append(prev, l)
		a += l.Size
	}
	if len(prev) > before {
		prev = prev[len(prev)-before:]
	}
	return append(prev, m.Disasm(pc, after+1)...)
}

// walk は from から命令を順に読み、to 以上になった位置と命令数を返す。
func (m *Machine) walk(from, to uint64) (uint64, int) {
	n := 0
	for from < to {
		in, ok := m.DecodeAt(from)
		if !ok {
			return 0, 0
		}
		from += in.Size
		n++
	}
	return from, n
}

// DisasmInst は 1 命令を "addi a0, a0, 1" の形にする (pc は分岐先の計算用)。
func DisasmInst(in Inst, pc uint64) string { return disasmBody(in, pc) }

// Symbolize は addr を "func+0x10" の形にする (シンボルが無ければ空)。
func (m *Machine) Symbolize(addr uint64) string {
	sym, off, ok := m.Symbols.Lookup(addr)
	if !ok {
		return ""
	}
	if off == 0 {
		return sym.Name
	}
	return fmt.Sprintf("%s+0x%x", sym.Name, off)
}

// ---- 命令トレース (リングバッファ) ----

// TraceEntry は実行した 1 命令の記録。
type TraceEntry struct {
	Seq  uint64 // 何番目の命令か (instret)
	PC   uint64
	Priv uint64
	Raw  uint32 // 命令語 (圧縮命令なら 16 ビット)
	Size uint64
	Text string // 逆アセンブル
}

// TraceRing は直近 N 命令を記録するリングバッファ。記録はデコード済みの Inst ごと保存し、
// 文字列化は取り出すとき (Entries) だけ行う (実行を遅くしないため)。
type TraceRing struct {
	buf  []traceRec
	head int
	full bool
}

type traceRec struct {
	seq, pc, priv uint64
	in            Inst
}

func NewTraceRing(n int) *TraceRing { return &TraceRing{buf: make([]traceRec, n)} }

func (r *TraceRing) add(seq, pc, priv uint64, in Inst) {
	r.buf[r.head] = traceRec{seq, pc, priv, in}
	r.head++
	if r.head == len(r.buf) {
		r.head, r.full = 0, true
	}
}

// Entries は記録を古い順に返す。
func (r *TraceRing) Entries() []TraceEntry {
	recs := r.buf[:r.head]
	if r.full {
		recs = append(append([]traceRec(nil), r.buf[r.head:]...), r.buf[:r.head]...)
	}
	out := make([]TraceEntry, len(recs))
	for i, t := range recs {
		raw := t.in.Raw
		if t.in.Size == 2 {
			raw = t.in.Compressed
		}
		out[i] = TraceEntry{Seq: t.seq, PC: t.pc, Priv: t.priv, Raw: raw, Size: t.in.Size, Text: disasmBody(t.in, t.pc)}
	}
	return out
}

// EnableTrace は直近 n 命令のトレースを記録し始める (n <= 0 で停止)。
func (m *Machine) EnableTrace(n int) {
	if n <= 0 {
		m.CPU.Ring = nil
		return
	}
	m.CPU.Ring = NewTraceRing(n)
}

// Trace は記録された直近の命令を古い順に返す (EnableTrace していなければ nil)。
func (m *Machine) Trace() []TraceEntry {
	if m.CPU.Ring == nil {
		return nil
	}
	return m.CPU.Ring.Entries()
}
