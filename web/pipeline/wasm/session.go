package main

// ブラウザ (web/pipeline) 向けのセッション。syscall/js に依存しないので普通の go test で確かめられる。
//
// 流れ: compile (C → rvcc / minilang → minicc) → rvasm.BuildProgram (アセンブル + リンク)
//       → rv.NewUserMachine (ユーザモードのエミュレータ) で実行。
// JS とは JSON 文字列でやりとりする。

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"minicc/minicc"
	"rvasm/rvasm"
	"rvcc/rvcc"
	"rvemu/rv"
)

// ---- コンパイル ----

type compileReq struct {
	Lang string `json:"lang"` // "c" / "lang"
	Name string `json:"name"` // ソースの名前 (hello.c / fib.lang)
	Src  string `json:"src"`
	Opt  int    `json:"opt"` // C の最適化レベル 0..2
}

type errJSON struct {
	Stage string `json:"stage"` // "compile" / "asm" / "link"
	File  string `json:"file"`
	Line  int    `json:"line"`
	Col   int    `json:"col"`
	Msg   string `json:"msg"`
	Text  string `json:"text"` // 表示用の 1 行
	User  bool   `json:"user"` // ユーザのソースを指しているか (行をハイライトできる)
}

type asmFile struct {
	Name   string `json:"name"`
	Source string `json:"source"` // 元のソース名 (libc.c など。アセンブリ直書きなら "")
	User   bool   `json:"user"`   // ユーザのソースから生成されたか
	Text   string `json:"text"`
	Lines  []int  `json:"lines"` // アセンブリ行 → ソース行 (0 は対応なし)
}

type listJSON struct {
	Addr     uint64          `json:"addr"`
	Size     uint64          `json:"size"`
	Kind     string          `json:"kind"`
	Section  string          `json:"section"`
	File     int             `json:"file"` // files の添字
	Line     int             `json:"line"` // アセンブリの行
	SrcLine  int             `json:"src"`  // 元のソースの行 (0 は対応なし)
	Source   string          `json:"source"`
	Mnemonic string          `json:"mn,omitempty"`
	Pseudo   string          `json:"pseudo,omitempty"`
	Word     uint32          `json:"word"`
	Dis      string          `json:"dis,omitempty"` // エミュレータの逆アセンブラによる表示 (リロケーション適用後)
	Enc      *rvasm.Encoding `json:"enc,omitempty"`
	Bytes    string          `json:"bytes,omitempty"` // データの中身 (hex)
	Sym      string          `json:"sym,omitempty"`   // このアドレスに付いたラベル
	Local    string          `json:"lbl,omitempty"`   // このアドレスに付いたローカルラベル (.L…)
}

type relocJSON struct {
	rvasm.Reloc
	Index int `json:"index"` // 書き換えた命令の listing 添字 (-1 は不明)
}

type compileResult struct {
	OK       bool            `json:"ok"`
	Error    *errJSON        `json:"error,omitempty"`
	Lang     string          `json:"lang"`
	SrcName  string          `json:"srcName"`
	Files    []asmFile       `json:"files"`
	Listing  []listJSON      `json:"listing"`
	Relocs   []relocJSON     `json:"relocs"`
	Sections []rvasm.Section `json:"sections"`
	Symbols  []rvasm.Symbol  `json:"symbols"`
	Entry    uint64          `json:"entry"`
	ELF      *elfInfo        `json:"elf,omitempty"`
}

// build はソースを ELF までビルドし、可視化用の情報を返す。
func build(req compileReq) (*compileResult, *rvasm.Program) {
	res := &compileResult{Lang: req.Lang, SrcName: req.Name}
	var files []asmFile
	switch req.Lang {
	case "c":
		outs, err := rvcc.CompileAll([]rvcc.Source{{Name: req.Name, Text: req.Src}}, req.Opt)
		if err != nil {
			res.Error = toErr("compile", err, req.Name)
			return res, nil
		}
		for k, o := range outs {
			src := o.Source
			if src == o.Name {
				src = ""
			}
			files = append(files, asmFile{Name: o.Name, Source: src, User: k == len(outs)-1, Text: o.Asm, Lines: o.Lines})
		}
	case "lang":
		out, err := minicc.Compile(req.Src)
		if err != nil {
			res.Error = toErr("compile", err, req.Name)
			return res, nil
		}
		name := strings.TrimSuffix(req.Name, ".lang") + ".s"
		files = append(files, asmFile{Name: name, Source: req.Name, User: true, Text: out.Asm, Lines: out.Lines},
			asmFile{Name: "runtime.s", Text: minicc.Runtime()})
	default:
		res.Error = &errJSON{Stage: "compile", Msg: "unknown language " + req.Lang, Text: "unknown language " + req.Lang}
		return res, nil
	}
	for k := range files {
		if files[k].Lines == nil {
			files[k].Lines = make([]int, strings.Count(files[k].Text, "\n")+1)
		}
	}
	res.Files = files
	var srcs []rvasm.Source
	for _, f := range files {
		srcs = append(srcs, rvasm.Source{Name: f.Name, Text: f.Text})
	}
	p, err := rvasm.BuildProgram(srcs...)
	if err != nil {
		e := toErr("link", err, "")
		var ae *rvasm.Error
		if errors.As(err, &ae) {
			e.Stage = "asm"
			// ユーザのアセンブリの行なら元のソースの行に引き直す
			for _, f := range files {
				if f.Name == ae.File && f.User && ae.Line-1 < len(f.Lines) && f.Lines[ae.Line-1] > 0 {
					e.User, e.File, e.Line, e.Col = true, req.Name, f.Lines[ae.Line-1], 1
				}
			}
		}
		res.Error = e
		return res, nil
	}
	fileIdx := map[string]int{}
	for k, f := range files {
		fileIdx[f.Name] = k
	}
	labels, locals := map[uint64]string{}, map[uint64]string{}
	for _, s := range p.Symbols {
		if s.Section != ".text" {
			continue
		}
		if !strings.HasPrefix(s.Name, ".L") {
			labels[s.Value] = s.Name
		} else if !strings.HasPrefix(s.Name, ".Lpcrel_hi") {
			locals[s.Value] = s.Name
		}
	}
	idxOf := map[uint64]int{}
	for k, l := range p.Listing {
		fi := fileIdx[l.File]
		j := listJSON{Addr: l.Addr, Size: l.Size, Kind: l.Kind, Section: l.Section, File: fi, Line: l.Line,
			Source: l.Source, Mnemonic: l.Mnemonic, Pseudo: l.Pseudo, Word: l.Word, Enc: l.Enc, Sym: labels[l.Addr], Local: locals[l.Addr]}
		if f := files[fi]; l.Line >= 1 && l.Line <= len(f.Lines) {
			j.SrcLine = f.Lines[l.Line-1]
		}
		if l.Kind == "inst" {
			j.Dis = rv.DisasmInst(rv.Decode(l.Word), l.Addr)
			idxOf[l.Addr] = k
		} else if l.Bytes != nil {
			b := l.Bytes
			if len(b) > 64 {
				b = b[:64]
			}
			j.Bytes = hex.EncodeToString(b)
		}
		res.Listing = append(res.Listing, j)
	}
	for _, r := range p.Relocs {
		idx := -1
		if k, ok := idxOf[r.Addr]; ok {
			idx = k
		}
		res.Relocs = append(res.Relocs, relocJSON{Reloc: r, Index: idx})
	}
	res.Sections, res.Symbols, res.Entry = p.Sections, p.Symbols, p.Entry
	sort.SliceStable(res.Symbols, func(i, j int) bool { return res.Symbols[i].Value < res.Symbols[j].Value })
	res.ELF = parseELF(p.ELF)
	res.OK = true
	return res, p
}

func toErr(stage string, err error, srcName string) *errJSON {
	e := &errJSON{Stage: stage, Msg: err.Error(), Text: err.Error()}
	var ce *rvcc.Error
	var le *minicc.LangError
	var ae *rvasm.Error
	switch {
	case errors.As(err, &ce):
		e.File, e.Line, e.Col, e.Msg = ce.File, ce.Line, ce.Col, ce.Msg
		e.User = ce.File == srcName
	case errors.As(err, &le):
		e.File, e.Line, e.Col, e.Msg = srcName, le.Pos.Line, le.Pos.Col, le.Kind+": "+le.Msg
		e.Text = fmt.Sprintf("%s:%d:%d: %s", srcName, le.Pos.Line, le.Pos.Col, e.Msg)
		e.User = le.Pos.Line > 0
	case errors.As(err, &ae):
		e.File, e.Line, e.Col, e.Msg = ae.File, ae.Line, ae.Col, ae.Msg
	}
	return e
}

// ---- ELF の解析 (表示用。ヘッダとセクション・プログラムヘッダだけを素朴に読む) ----

type elfInfo struct {
	Size      int        `json:"size"`
	Hex       string     `json:"hex"`
	Entry     uint64     `json:"entry"`
	Type      uint16     `json:"type"`
	Machine   uint16     `json:"machine"`
	Phoff     uint64     `json:"phoff"`
	Shoff     uint64     `json:"shoff"`
	Phnum     int        `json:"phnum"`
	Shnum     int        `json:"shnum"`
	Phentsize int        `json:"phentsize"`
	Shentsize int        `json:"shentsize"`
	Segments  []elfSeg   `json:"segments"`
	Sections  []elfSec   `json:"sections"`
	Ranges    []elfRange `json:"ranges"` // 色分け用 (ファイル上の範囲)
}

type elfSeg struct {
	Type   string `json:"type"`
	Offset uint64 `json:"offset"`
	Vaddr  uint64 `json:"vaddr"`
	Filesz uint64 `json:"filesz"`
	Memsz  uint64 `json:"memsz"`
	Flags  string `json:"flags"`
	Align  uint64 `json:"align"`
}

type elfSec struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Addr   uint64 `json:"addr"`
	Offset uint64 `json:"offset"`
	Size   uint64 `json:"size"`
	Flags  string `json:"flags"`
}

type elfRange struct {
	Name  string `json:"name"`
	Off   uint64 `json:"off"`
	Size  uint64 `json:"size"`
	Color string `json:"color"` // CSS 変数名の番号 (c1..c6) か "muted"
}

func parseELF(b []byte) *elfInfo {
	if len(b) < 64 {
		return nil
	}
	le := binary.LittleEndian
	e := &elfInfo{Size: len(b), Hex: hex.EncodeToString(b), Type: le.Uint16(b[16:]), Machine: le.Uint16(b[18:]),
		Entry: le.Uint64(b[24:]), Phoff: le.Uint64(b[32:]), Shoff: le.Uint64(b[40:]),
		Phentsize: int(le.Uint16(b[54:])), Phnum: int(le.Uint16(b[56:])),
		Shentsize: int(le.Uint16(b[58:])), Shnum: int(le.Uint16(b[60:]))}
	shstrndx := int(le.Uint16(b[62:]))
	e.Ranges = append(e.Ranges, elfRange{"ELF ヘッダ", 0, 64, "c1"})
	if e.Phnum > 0 {
		e.Ranges = append(e.Ranges, elfRange{"プログラムヘッダ", e.Phoff, uint64(e.Phnum * e.Phentsize), "c2"})
	}
	for i := 0; i < e.Phnum; i++ {
		o := int(e.Phoff) + i*e.Phentsize
		if o+56 > len(b) {
			break
		}
		typ := le.Uint32(b[o:])
		fl := le.Uint32(b[o+4:])
		flags := ""
		for _, f := range []struct {
			bit uint32
			c   string
		}{{4, "R"}, {2, "W"}, {1, "X"}} {
			if fl&f.bit != 0 {
				flags += f.c
			} else {
				flags += "-"
			}
		}
		tn := map[uint32]string{0: "NULL", 1: "LOAD", 2: "DYNAMIC", 3: "INTERP", 4: "NOTE", 6: "PHDR", 0x6474e551: "GNU_STACK"}[typ]
		if tn == "" {
			tn = fmt.Sprintf("0x%x", typ)
		}
		e.Segments = append(e.Segments, elfSeg{Type: tn, Flags: flags, Offset: le.Uint64(b[o+8:]), Vaddr: le.Uint64(b[o+16:]),
			Filesz: le.Uint64(b[o+32:]), Memsz: le.Uint64(b[o+40:]), Align: le.Uint64(b[o+48:])})
	}
	type rawSec struct {
		name                   uint32
		typ                    uint32
		flags, addr, off, size uint64
	}
	var secs []rawSec
	for i := 0; i < e.Shnum; i++ {
		o := int(e.Shoff) + i*e.Shentsize
		if o+64 > len(b) {
			break
		}
		secs = append(secs, rawSec{le.Uint32(b[o:]), le.Uint32(b[o+4:]), le.Uint64(b[o+8:]), le.Uint64(b[o+16:]),
			le.Uint64(b[o+24:]), le.Uint64(b[o+32:])})
	}
	name := func(off uint32) string {
		if shstrndx >= len(secs) {
			return ""
		}
		s := secs[shstrndx]
		p := s.off + uint64(off)
		if p >= uint64(len(b)) {
			return ""
		}
		end := p
		for end < uint64(len(b)) && b[end] != 0 {
			end++
		}
		return string(b[p:end])
	}
	colors := map[string]string{".text": "c3", ".rodata": "c4", ".data": "c6", ".sdata": "c6"}
	for i, s := range secs {
		if i == 0 {
			continue
		}
		n := name(s.name)
		tn := map[uint32]string{1: "PROGBITS", 2: "SYMTAB", 3: "STRTAB", 4: "RELA", 8: "NOBITS"}[s.typ]
		if tn == "" {
			tn = fmt.Sprintf("0x%x", s.typ)
		}
		fl := ""
		if s.flags&2 != 0 {
			fl += "A"
		}
		if s.flags&1 != 0 {
			fl += "W"
		}
		if s.flags&4 != 0 {
			fl += "X"
		}
		e.Sections = append(e.Sections, elfSec{Name: n, Type: tn, Addr: s.addr, Offset: s.off, Size: s.size, Flags: fl})
		if s.typ != 8 && s.size > 0 {
			c := colors[n]
			if c == "" {
				c = "muted"
			}
			e.Ranges = append(e.Ranges, elfRange{n, s.off, s.size, c})
		}
	}
	if e.Shnum > 0 {
		e.Ranges = append(e.Ranges, elfRange{"セクションヘッダ", e.Shoff, uint64(e.Shnum * e.Shentsize), "c5"})
	}
	sort.SliceStable(e.Ranges, func(i, j int) bool { return e.Ranges[i].Off < e.Ranges[j].Off })
	return e
}

// ---- 実行 ----

type chunk struct {
	Fd   int    `json:"fd"`
	Text string `json:"text"`
}

type chunkWriter struct {
	dst *[]chunk
	fd  int
}

func (w chunkWriter) Write(p []byte) (int, error) {
	c := w.dst
	if n := len(*c); n > 0 && (*c)[n-1].Fd == w.fd {
		(*c)[n-1].Text += string(p)
	} else {
		*c = append(*c, chunk{w.fd, string(p)})
	}
	return len(p), nil
}

type syscallJSON struct {
	Seq  uint64 `json:"seq"`
	PC   uint64 `json:"pc"`
	Num  uint64 `json:"num"`
	Name string `json:"name"`
	Args string `json:"args"`
	Ret  string `json:"ret"`
	Idx  int    `json:"idx"` // ecall 命令の listing 添字
}

var syscallNames = map[uint64]string{29: "ioctl", 63: "read", 64: "write", 66: "writev", 93: "exit", 94: "exit_group",
	96: "set_tid_address", 113: "clock_gettime", 134: "rt_sigaction", 135: "rt_sigprocmask", 172: "getpid",
	178: "gettid", 214: "brk", 215: "munmap", 222: "mmap", 226: "mprotect"}

const maxSyscallLog = 2000

type session struct {
	res   *compileResult
	prog  *rvasm.Program
	idxOf map[uint64]int // 命令のアドレス → listing 添字
	user  map[int]bool   // ユーザのファイルの添字

	m        *rv.Machine
	out      []chunk
	sys      []syscallJSON
	sysCount int
	budget   uint64 // 命令数の上限 (無限ループ対策)
	stdin    string

	bench *rv.Machine
	bout  []chunk
}

func newSession() *session { return &session{budget: 500_000_000} }

func (s *session) compile(reqJSON string) string {
	var req compileReq
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		return toJSON(compileResult{Error: &errJSON{Msg: err.Error(), Text: err.Error()}})
	}
	res, p := build(req)
	s.m = nil
	if !res.OK {
		s.res, s.prog = nil, nil
		return toJSON(res)
	}
	s.res, s.prog = res, p
	s.idxOf = map[uint64]int{}
	for k, l := range res.Listing {
		if l.Kind == "inst" {
			s.idxOf[l.Addr] = k
		}
	}
	s.user = map[int]bool{}
	for k, f := range res.Files {
		if f.User {
			s.user[k] = true
		}
	}
	return toJSON(res)
}

// parseArgs は `a "b c" d` を ["a", "b c", "d"] にする。
func parseArgs(spec string) []string {
	var args []string
	for i, part := range strings.Split(spec, `"`) {
		if i%2 == 1 {
			args = append(args, part)
		} else {
			args = append(args, strings.Fields(part)...)
		}
	}
	return args
}

// start は直前にビルドした ELF でマシンを作り直す。args は argv[1:] (空白区切り、"..." でまとめる)。
func (s *session) start(args, stdin string, budget uint64) string {
	if s.prog == nil {
		return "まだビルドしていません"
	}
	if budget > 0 {
		s.budget = budget
	}
	s.out, s.sys, s.sysCount, s.stdin = nil, nil, 0, stdin
	argv := append([]string{strings.TrimSuffix(strings.TrimSuffix(s.res.SrcName, ".c"), ".lang")}, parseArgs(args)...)
	m, err := rv.NewUserMachine(s.prog.ELF, argv, chunkWriter{&s.out, 1}, strings.NewReader(stdin))
	if err != nil {
		s.m = nil
		return err.Error()
	}
	m.Proc.Stderr = chunkWriter{&s.out, 2}
	orig := m.CPU.Syscall
	m.CPU.Syscall = func(c *rv.CPU) {
		pc, num := c.PC, c.X[17]
		a0, a1, a2 := c.X[10], c.X[11], c.X[12]
		orig(c)
		s.sysCount++
		if len(s.sys) >= maxSyscallLog {
			return
		}
		e := syscallJSON{Seq: c.Steps, PC: pc, Num: num, Name: syscallNames[num], Idx: -1}
		if k, ok := s.idxOf[pc]; ok {
			e.Idx = k
		}
		switch num {
		case 64:
			text := string(m.ReadMem(a1, min(a2, 48)))
			if a2 > 48 {
				text += "…"
			}
			e.Args = fmt.Sprintf("fd=%d, buf=0x%x %q, len=%d", a0, a1, text, a2)
		case 63:
			e.Args = fmt.Sprintf("fd=%d, buf=0x%x, len=%d", a0, a1, a2)
		case 93, 94:
			e.Args = fmt.Sprintf("status=%d", int64(a0))
		case 214:
			e.Args = fmt.Sprintf("addr=0x%x", a0)
		default:
			e.Args = fmt.Sprintf("0x%x, 0x%x, 0x%x", a0, a1, a2)
		}
		if c.Halted {
			e.Ret = "-"
		} else if num == 214 || num == 222 {
			e.Ret = fmt.Sprintf("0x%x", c.X[10])
		} else {
			e.Ret = fmt.Sprint(int64(c.X[10]))
		}
		s.sys = append(s.sys, e)
	}
	s.m = m
	return ""
}

type runResult struct {
	Steps   uint64  `json:"steps"`
	Instret uint64  `json:"instret"`
	Out     []chunk `json:"out"`
	Exited  bool    `json:"exited"`
	Code    int     `json:"code"`
	Err     string  `json:"err"`
	Budget  bool    `json:"budget"` // 命令数の上限に達して止めた
}

func (s *session) result(before uint64) string {
	m := s.m
	r := runResult{Steps: m.CPU.Steps - before, Instret: m.CPU.Steps, Out: s.out, Exited: m.CPU.Halted, Code: m.CPU.ExitCode}
	if m.CPU.Err != nil {
		r.Err = m.CPU.Err.Error()
	}
	if !r.Exited && m.CPU.Steps >= s.budget {
		r.Budget = true
	}
	s.out = nil
	return toJSON(r)
}

// run は最大 max 命令実行する (上限 budget を超えない)。
func (s *session) run(max uint64) string {
	if s.m == nil {
		return toJSON(runResult{Err: "not started", Exited: true})
	}
	before := s.m.CPU.Steps
	if left := s.budget - min(before, s.budget); max > left {
		max = left
	}
	if max > 0 {
		s.m.Run(max)
	}
	return s.result(before)
}

// srcLine は pc の命令を生んだユーザのソースの行 (無ければ 0)。
func (s *session) srcLine(pc uint64) int {
	k, ok := s.idxOf[pc]
	if !ok {
		return 0
	}
	l := s.res.Listing[k]
	if !s.user[l.File] {
		return 0
	}
	return l.SrcLine
}

// stepLine はユーザのソースで「次の行」に着くまで実行する (最大 max 命令)。
// libc / ランタイムの中は一気に進める。
func (s *session) stepLine(max uint64) string {
	if s.m == nil {
		return toJSON(runResult{Err: "not started", Exited: true})
	}
	m := s.m
	before := m.CPU.Steps
	start := s.srcLine(m.CPU.PC)
	for n := uint64(0); n < max && !m.CPU.Halted && m.CPU.Steps < s.budget; n++ {
		m.Step()
		if l := s.srcLine(m.CPU.PC); l != 0 && l != start {
			break
		}
	}
	return s.result(before)
}

type stateJSON struct {
	X       []string `json:"x"`
	PC      uint64   `json:"pc"`
	Idx     int      `json:"idx"` // pc の listing 添字 (-1 は不明)
	Sym     string   `json:"sym"`
	Instret uint64   `json:"instret"`
	Halted  bool     `json:"halted"`
	Code    int      `json:"code"`
	Err     string   `json:"err"`
	Sys     int      `json:"sysCount"`
}

func (s *session) state() string {
	if s.m == nil {
		return "null"
	}
	snap := s.m.Snapshot()
	st := stateJSON{PC: snap.PC, Idx: -1, Sym: s.m.Symbolize(snap.PC), Instret: snap.Instret, Halted: snap.Halted,
		Code: snap.ExitCode, Err: snap.Err, Sys: s.sysCount}
	if k, ok := s.idxOf[snap.PC]; ok {
		st.Idx = k
	}
	for _, x := range snap.X {
		st.X = append(st.X, fmt.Sprintf("%016x", x))
	}
	return toJSON(st)
}

func (s *session) syscalls(from int) string {
	if from < 0 || from > len(s.sys) {
		from = len(s.sys)
	}
	return toJSON(s.sys[from:])
}

// ---- 比較 (fib(25) を C -O0 / -O2 と minilang で) ----

type benchInfo struct {
	Error  string `json:"error,omitempty"`
	Static int    `json:"static"` // fib 関数の静的な命令数 (機械語の命令語の数)
	Lines  int    `json:"lines"`  // 同じ範囲のアセンブリの命令行の数 (擬似命令 call / la は 1 行で 2 命令)
	Func   string `json:"func"`   // fib 関数のラベル
}

var reLangFib = regexp.MustCompile(`^fn_\d+_fib$`)

// staticCount は名前が match する関数の命令数 (次の非ローカルラベルまで) を数える。
func staticCount(p *rvasm.Program, file string, match func(string) bool) (n, lines int, fn string) {
	var start uint64
	name := ""
	for _, s := range p.Symbols {
		if s.File == file && s.Section == ".text" && match(s.Name) {
			start, name = s.Value, s.Name
		}
	}
	if name == "" {
		return 0, 0, ""
	}
	end := ^uint64(0)
	for _, s := range p.Symbols {
		if s.Section == ".text" && !strings.HasPrefix(s.Name, ".L") && s.Value > start && s.Value < end {
			end = s.Value
		}
	}
	seen := map[int]bool{}
	for _, l := range p.Listing {
		if l.Kind == "inst" && l.Addr >= start && l.Addr < end {
			n++
			if !seen[l.Line] {
				seen[l.Line] = true
				lines++
			}
		}
	}
	return n, lines, name
}

// benchStart は比較用のプログラムをビルドしてマシンを用意する。lang は "c" / "lang"。
func (s *session) benchStart(lang, name, src string, opt int) string {
	s.bench, s.bout = nil, nil
	res, p := build(compileReq{Lang: lang, Name: name, Src: src, Opt: opt})
	if !res.OK {
		return toJSON(benchInfo{Error: res.Error.Text})
	}
	var userFile string
	for _, f := range res.Files {
		if f.User {
			userFile = f.Name
		}
	}
	match := func(n string) bool { return n == "fib" }
	if lang == "lang" {
		match = reLangFib.MatchString
	}
	info := benchInfo{}
	info.Static, info.Lines, info.Func = staticCount(p, userFile, match)
	m, err := rv.NewUserMachine(p.ELF, []string{"fib25"}, chunkWriter{&s.bout, 1}, strings.NewReader(""))
	if err != nil {
		return toJSON(benchInfo{Error: err.Error()})
	}
	m.Proc.Stderr = chunkWriter{&s.bout, 2}
	s.bench = m
	return toJSON(info)
}

type benchResult struct {
	Done    bool   `json:"done"`
	Instret uint64 `json:"instret"`
	Out     string `json:"out"`
	Code    int    `json:"code"`
	Err     string `json:"err"`
}

func (s *session) benchRun(max uint64) string {
	m := s.bench
	if m == nil {
		return toJSON(benchResult{Done: true, Err: "not started"})
	}
	m.Run(max)
	r := benchResult{Done: m.CPU.Halted, Instret: m.CPU.Steps, Code: m.CPU.ExitCode}
	if m.CPU.Steps >= 2_000_000_000 {
		r.Done, r.Err = true, "instruction limit"
	}
	if m.CPU.Err != nil {
		r.Err = m.CPU.Err.Error()
	}
	if r.Done {
		for _, c := range s.bout {
			r.Out += c.Text
		}
	}
	return toJSON(r)
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":` + fmt.Sprintf("%q", err.Error()) + `}`
	}
	return string(b)
}
