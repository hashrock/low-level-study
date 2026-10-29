// Package rvasm は学習用の RISC-V (RV64IM) アセンブラ + 静的リンカ。
//
// ファイルシステムには触らない純粋な関数だけで構成されている
// (ブラウザ上の WebAssembly からもそのまま使える)。
//
//	obj, err := rvasm.Assemble("a.s", src)   // .s → オブジェクト (ELF .o は obj.ELF())
//	exe, err := rvasm.Link(obj1, obj2)       // オブジェクト → 静的 ELF 実行ファイル
//	exe, err := rvasm.Build(rvasm.Source{Name: "a.s", Text: src}, ...)  // 上の 2 つを一度に
//
// 可視化用には LinkProgram / BuildProgram が、ELF に加えて
// 命令ごとのリスティング (アドレス・命令語・ソース行・形式とビットフィールド) と
// 解決済みリロケーションの一覧を返す。
package rvasm

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// Source は 1 つのアセンブリソース (ファイル名と中身)。
type Source struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Error は位置つきのエラー。アセンブルエラーと、ソース行が分かるリンクエラー
// (未定義シンボル、範囲外の分岐など) はこの型で返る。
type Error struct {
	File string `json:"file"`
	Line int    `json:"line"` // 1 始まり
	Col  int    `json:"col"`  // 1 始まり (バイト単位)。命令名の位置
	Msg  string `json:"msg"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Col, e.Msg)
}

// Object はアセンブル済みのオブジェクト (= .o ファイル 1 つ分)。
type Object struct {
	Name string
	obj  *object
	elf  []byte
}

// Assemble はアセンブリソースをオブジェクトにする。
//
// 中身は一度 ELF .o に書き出して読み直したもの (CLI の as → ld と同じ経路) で、
// ソース行の情報 (Listing) だけをメモリ上で引き継ぐ。
func Assemble(name, src string) (*Object, error) {
	o, err := assemble(name, src)
	if err != nil {
		return nil, err
	}
	b, err := writeObject(o)
	if err != nil {
		return nil, err
	}
	ro, err := readObject(name, b)
	if err != nil {
		return nil, err
	}
	for _, s := range ro.sections {
		if orig := o.section(s.name); orig != nil {
			s.lines = orig.lines
		}
	}
	return &Object{Name: name, obj: ro, elf: b}, nil
}

// ReadObject は ELF .o のバイト列を読み込む (ソース行の情報は持たない)。
func ReadObject(name string, data []byte) (*Object, error) {
	o, err := readObject(name, data)
	if err != nil {
		return nil, err
	}
	return &Object{Name: name, obj: o, elf: append([]byte(nil), data...)}, nil
}

// ELF はオブジェクトの ELF .o 形式のバイト列を返す。
func (o *Object) ELF() []byte { return o.elf }

// Listing はセクション内オフセットでのリスティングを返す (Addr = Offset、命令語はリロケーション前)。
func (o *Object) Listing() []Line {
	var out []Line
	for _, s := range o.obj.sections {
		out = append(out, listSection(o.Name, s, s.data, 0)...)
	}
	return out
}

// Relocs は未解決のリロケーション一覧を返す (Addr = セクション内オフセット)。
func (o *Object) Relocs() []Reloc {
	var out []Reloc
	for _, s := range o.obj.sections {
		for _, r := range s.relocs {
			if r.typ == rRelax || r.typ == rNone {
				continue
			}
			out = append(out, Reloc{File: o.Name, Section: s.name, Offset: r.off, Addr: r.off,
				Line: lineOf(s, r.off), Type: relocName(r.typ), Symbol: r.sym, Addend: r.addend, Size: relocSize(r.typ)})
		}
	}
	return out
}

// Symbols はオブジェクトのシンボル一覧 (Value はセクション内オフセットか .equ の値)。
func (o *Object) Symbols() []Symbol {
	var out []Symbol
	for _, s := range o.obj.symbols {
		if s.sec != "" && s.name == s.sec {
			continue
		}
		out = append(out, Symbol{Name: s.name, File: o.Name, Section: s.sec, Value: uint64(s.value),
			Global: s.global, Defined: s.defined, Abs: s.abs})
	}
	return out
}

// Line はリスティングの 1 行 (命令 1 つ、またはデータ 1 まとまり)。
type Line struct {
	Kind     string    `json:"kind"` // "inst" か "data"
	File     string    `json:"file"`
	Section  string    `json:"section"` // 入力セクション名 (.text など)
	Offset   uint64    `json:"offset"`  // 入力セクション内オフセット
	Addr     uint64    `json:"addr"`    // 仮想アドレス (リンク前は Offset と同じ)
	Size     uint64    `json:"size"`
	Line     int       `json:"line"` // ソースの行 (1 始まり)
	Col      int       `json:"col"`
	Source   string    `json:"source"`             // ソースの行 (前後の空白を除く)
	Mnemonic string    `json:"mnemonic,omitempty"` // 機械命令名 (擬似命令は展開後: li → addi など)
	Pseudo   string    `json:"pseudo,omitempty"`   // 元の命令名が擬似命令ならその名前
	Word     uint32    `json:"word"`               // 命令語 (Kind == "inst")
	Enc      *Encoding `json:"enc,omitempty"`      // 命令語の分解 (Kind == "inst")
	Bytes    []byte    `json:"bytes,omitempty"`    // データの中身 (Kind == "data"、.bss では nil)
}

// Reloc はリロケーション 1 つ。Program.Relocs では解決済みの値が入る。
type Reloc struct {
	File    string `json:"file"`
	Section string `json:"section"`
	Offset  uint64 `json:"offset"` // 入力セクション内オフセット
	Addr    uint64 `json:"addr"`   // P: 書き換える位置の仮想アドレス
	Line    int    `json:"line"`   // その位置を生んだソース行 (不明なら 0)
	Type    string `json:"type"`   // "R_RISCV_HI20" など
	Symbol  string `json:"symbol"`
	Addend  int64  `json:"addend"`
	Size    int    `json:"size"` // 書き換えるバイト数 (CALL は auipc+jalr で 8)
	// 以下はリンク後のみ
	SymAddr uint64 `json:"symAddr"` // S: シンボルのアドレス
	Value   int64  `json:"value"`   // 埋め込んだ値: 絶対系は S+A、PC 相対系は S+A-P、PCREL_LO は対応する auipc のオフセット
	Before  uint64 `json:"before"`  // 書き換え前の値 (リトルエンディアンで Size バイト)
	After   uint64 `json:"after"`   // 書き換え後の値
}

// Symbol はシンボル 1 つ。
type Symbol struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Section string `json:"section"` // 定義されたセクション ("" なら未定義か .equ)
	Value   uint64 `json:"value"`   // Program ではアドレス
	Global  bool   `json:"global"`
	Defined bool   `json:"defined"`
	Abs     bool   `json:"abs"` // .equ による絶対シンボル
}

// Section は出力 ELF のセクション。
type Section struct {
	Name   string `json:"name"`
	Addr   uint64 `json:"addr"`
	Offset uint64 `json:"offset"` // ファイル上のオフセット (.bss は 0)
	Size   uint64 `json:"size"`
}

// Program はリンク結果: ELF 実行ファイルと、可視化用の情報。
type Program struct {
	ELF      []byte    `json:"-"`
	Entry    uint64    `json:"entry"`
	Sections []Section `json:"sections"`
	Symbols  []Symbol  `json:"symbols"`
	Listing  []Line    `json:"listing"` // アドレス順。命令語はリロケーション適用後
	Relocs   []Reloc   `json:"relocs"`
}

// Link はオブジェクトをリンクして静的 ELF 実行ファイル (エントリは _start) を返す。
func Link(objs ...*Object) ([]byte, error) {
	p, err := LinkProgram(objs...)
	if err != nil {
		return nil, err
	}
	return p.ELF, nil
}

// Build はソースをアセンブルしてリンクし、ELF 実行ファイルを返す。
func Build(srcs ...Source) ([]byte, error) {
	p, err := BuildProgram(srcs...)
	if err != nil {
		return nil, err
	}
	return p.ELF, nil
}

// BuildProgram は Build と同じだが、リスティング等の情報も返す。
func BuildProgram(srcs ...Source) (*Program, error) {
	var objs []*Object
	for _, s := range srcs {
		o, err := Assemble(s.Name, s.Text)
		if err != nil {
			return nil, err
		}
		objs = append(objs, o)
	}
	return LinkProgram(objs...)
}

// LinkProgram は Link と同じだが、リスティング等の情報も返す。
func LinkProgram(objs ...*Object) (*Program, error) {
	in := make([]*object, len(objs))
	for k, o := range objs {
		in[k] = o.obj
	}
	r, err := linkDetailed(in)
	if err != nil {
		return nil, err
	}
	p := &Program{ELF: r.elf, Entry: r.entry}
	names := map[string]string{"text": ".text", "rodata": ".rodata", "data": ".data", "bss": ".bss"}
	for _, k := range outKinds {
		s := r.outs[k]
		if s.size == 0 {
			continue
		}
		sec := Section{Name: names[k], Addr: s.addr, Offset: s.off, Size: s.size}
		if k == "bss" {
			sec.Offset = 0
		}
		p.Sections = append(p.Sections, sec)
	}
	for k, o := range in {
		name := objs[k].Name
		for _, s := range o.sections {
			pl := r.place[s]
			var data []byte
			if !s.nobits {
				data = pl.out.data[pl.off : pl.off+s.size]
			}
			p.Listing = append(p.Listing, listSection(name, s, data, pl.out.addr+pl.off)...)
		}
		for _, s := range o.symbols {
			if !s.defined || (s.sec != "" && s.name == s.sec) {
				continue
			}
			p.Symbols = append(p.Symbols, Symbol{Name: s.name, File: name, Section: s.sec,
				Value: r.symAddr(o, s), Global: s.global, Defined: true, Abs: s.abs})
		}
	}
	sort.SliceStable(p.Listing, func(i, j int) bool { return p.Listing[i].Addr < p.Listing[j].Addr })
	fileOf := map[*object]string{}
	for k, o := range in {
		fileOf[o] = objs[k].Name
	}
	for _, fx := range r.fixups {
		v := int64(fx.s) + fx.r.addend
		switch fx.r.typ {
		case rBranch, rJal, rCall, rCallPLT, rPcrelHi20:
			v -= int64(fx.p)
		case rPcrelLo12I, rPcrelLo12S:
			v = r.pcrelHi[uint64(v)]
		}
		p.Relocs = append(p.Relocs, Reloc{
			File: fileOf[fx.o], Section: fx.sec.name, Offset: fx.r.off, Addr: fx.p, Line: lineOf(fx.sec, fx.r.off),
			Type: relocName(fx.r.typ), Symbol: fx.r.sym, Addend: fx.r.addend, Size: relocSize(fx.r.typ),
			SymAddr: fx.s, Value: v, Before: fx.before, After: fx.after,
		})
	}
	return p, nil
}

// listSection はセクション s のリスティングを作る。data はセクションの中身 (リンク後なら適用済み)。
func listSection(file string, s *section, data []byte, base uint64) []Line {
	var out []Line
	for _, e := range s.lines {
		l := Line{Kind: "data", File: file, Section: s.name, Offset: e.off, Addr: base + e.off, Size: e.size}
		if e.st != nil {
			l.Line, l.Col, l.Source = e.st.line, e.st.col, e.st.text
		}
		if e.inst {
			l.Kind, l.Mnemonic = "inst", e.op
			if e.st != nil && e.st.name != e.op {
				l.Pseudo = e.st.name
			}
			if int(e.off)+4 <= len(data) {
				l.Word = binary.LittleEndian.Uint32(data[e.off:])
				enc := Decode(l.Word)
				l.Enc = &enc
			}
		} else if data != nil && e.off+e.size <= uint64(len(data)) {
			l.Bytes = append([]byte(nil), data[e.off:e.off+e.size]...)
		}
		out = append(out, l)
	}
	return out
}

func lineOf(s *section, off uint64) int {
	for _, e := range s.lines {
		if e.off <= off && off < e.off+e.size && e.st != nil {
			return e.st.line
		}
	}
	return 0
}

func relocName(typ uint32) string {
	if n, ok := relocNames[typ]; ok {
		return n
	}
	return fmt.Sprintf("R_RISCV_%d", typ)
}
