package rvasm

// asm.go — 2 パスアセンブラ本体。
//
//	パス 1: 全文を走査し、各命令・データのサイズだけを数えてラベルのオフセットを決める。
//	パス 2: 同じ走査をもう一度行い、今度は実際にバイト列をエンコードする。
//	        ラベルの値はパス 1 で確定しているので、前方参照の分岐も解決できる。
//
// 同じセクション内への分岐 (beq/jal) はここで解決し、それ以外 (別セクション・
// 別ファイルのシンボル、絶対アドレス) はリロケーションとしてリンカに任せる。

import (
	"encoding/binary"
	"fmt"
	"strings"
)

type section struct {
	name   string
	data   []byte
	size   uint64 // 現在のサイズ (.bss では data を持たずサイズだけ数える)
	align  uint64
	nobits bool
	relocs []reloc
	lines  []entry // リスティング (パス 2 で記録。.o を経由すると失われる)
}

// entry はリスティングの 1 行: セクション内のある範囲を生んだソース行。
type entry struct {
	off  uint64
	size uint64
	inst bool   // 命令なら true、データ (.word / .ascii / .align の詰め物など) なら false
	op   string // 機械命令名 (擬似命令展開後)
	st   *stmt  // 元の文
}

type reloc struct {
	off    uint64 // セクション先頭からのオフセット
	typ    uint32 // R_RISCV_*
	sym    string
	addend int64
}

type symbol struct {
	name    string
	sec     string // 定義されたセクション名 (未定義なら "")
	value   int64  // セクション内オフセット、または .equ の値
	abs     bool   // .equ による絶対シンボル
	global  bool
	defined bool
}

// object はアセンブル結果 (= .o ファイルの中身) 。リンカも同じ構造体を使う。
type object struct {
	name     string
	sections []*section
	symbols  []*symbol
}

func (o *object) section(name string) *section {
	for _, s := range o.sections {
		if s.name == name {
			return s
		}
	}
	return nil
}

func (o *object) symbol(name string) *symbol {
	for _, s := range o.symbols {
		if s.name == name {
			return s
		}
	}
	return nil
}

type assembler struct {
	obj    *object
	syms   map[string]*symbol
	equ    map[string]int64
	cur    *section
	final  bool  // パス 2 なら true
	labelN int   // la 用ローカルラベルの通し番号
	st     *stmt // 処理中の文 (リスティング用)
}

// assemble はソースをアセンブルしてオブジェクトを返す。
func assemble(name, src string) (*object, error) {
	stmts, err := parseSource(src)
	if err != nil {
		if e, ok := err.(*Error); ok {
			e.File = name
		}
		return nil, err
	}
	a := &assembler{obj: &object{name: name}, syms: map[string]*symbol{}}
	for pass := 1; pass <= 2; pass++ {
		a.final = pass == 2
		a.obj.sections = nil
		a.equ = map[string]int64{}
		a.labelN = 0
		a.switchSection(".text")
		for k := range stmts {
			a.st = &stmts[k]
			if err := a.statement(stmts[k]); err != nil {
				return nil, &Error{File: name, Line: stmts[k].line, Col: stmts[k].col, Msg: err.Error()}
			}
		}
		a.st = nil
	}
	return a.obj, nil
}

func (a *assembler) lookup(name string) (string, int64, bool) {
	if name == "." {
		return a.cur.name, int64(a.cur.size), true
	}
	if v, ok := a.equ[name]; ok {
		return "", v, true
	}
	if s := a.syms[name]; s != nil && s.defined && !s.abs {
		return s.sec, s.value, true
	}
	return "", 0, false
}

func (a *assembler) getSym(name string) *symbol {
	s, ok := a.syms[name]
	if !ok {
		s = &symbol{name: name}
		a.syms[name] = s
		a.obj.symbols = append(a.obj.symbols, s)
	}
	return s
}

func (a *assembler) switchSection(name string) {
	if s := a.obj.section(name); s != nil {
		a.cur = s
		return
	}
	a.cur = &section{name: name, align: 1, nobits: sectionKind(name) == "bss"}
	if name == ".text" {
		a.cur.align = 4
	}
	a.obj.sections = append(a.obj.sections, a.cur)
}

// defineLabel は現在位置にラベルを定義する。
func (a *assembler) defineLabel(name string) error {
	s := a.getSym(name)
	pc := int64(a.cur.size)
	if !a.final {
		if s.defined {
			return fmt.Errorf("symbol %q redefined", name)
		}
		s.defined, s.sec, s.value = true, a.cur.name, pc
		return nil
	}
	if s.sec != a.cur.name || s.value != pc {
		return fmt.Errorf("internal error: label %q moved between passes", name)
	}
	return nil
}

func (a *assembler) statement(st stmt) error {
	for _, l := range st.labels {
		if err := a.defineLabel(l); err != nil {
			return err
		}
	}
	if st.name == "" {
		return nil
	}
	if strings.HasPrefix(st.name, ".") {
		return a.directive(st)
	}
	if a.cur.nobits {
		return fmt.Errorf("instruction in %s", a.cur.name)
	}
	insts, err := expand(st, a.lookup, func() string {
		a.labelN++
		return fmt.Sprintf(".Lpcrel_hi%d", a.labelN)
	})
	if err != nil {
		return err
	}
	for _, in := range insts {
		if err := a.emitInst(in); err != nil {
			return err
		}
	}
	return nil
}

// emitInst は 1 命令をエンコードして現在のセクションに追加する。
func (a *assembler) emitInst(in inst) error {
	if in.label != "" {
		if err := a.defineLabel(in.label); err != nil {
			return err
		}
	}
	if !a.final { // パス 1 はサイズを数えるだけ
		a.cur.size += 4
		return nil
	}
	pc := int64(a.cur.size)
	f := opTable[in.op].f
	e := in.imm
	var imm int64
	switch {
	case in.rel != 0 && e.sym == "":
		return fmt.Errorf("%s: target must be a symbol", in.op)
	case in.rel != 0: // call/tail: 種別が決まっているリロケーション
		a.addReloc(in.rel, e)
	case e.mod != "" && e.sym == "": // %hi(定数) など: その場で計算
		switch e.mod {
		case "hi":
			imm = hi20(e.add)
		case "lo":
			imm = lo12(e.add)
		default:
			return fmt.Errorf("%%%s needs a symbol", e.mod)
		}
	case e.mod != "":
		typ := map[string]uint32{"hi": rHi20, "lo": rLo12I, "pcrel_hi": rPcrelHi20, "pcrel_lo": rPcrelLo12I}[e.mod]
		if f == fS { // ストア命令の即値は S 形式
			typ = map[uint32]uint32{rLo12I: rLo12S, rPcrelLo12I: rPcrelLo12S}[typ]
		}
		if typ == 0 || (f == fU) != (e.mod == "hi" || e.mod == "pcrel_hi") {
			return fmt.Errorf("%%%s cannot be used with %s", e.mod, in.op)
		}
		a.addReloc(typ, e)
	case e.sym != "" && (f == fB || f == fJ):
		// 分岐先が同じセクションで定義済みなら PC 相対オフセットを直接計算
		if sec, v, ok := a.lookup(e.sym); ok && sec == a.cur.name {
			imm = v + e.add - pc
		} else if f == fB {
			a.addReloc(rBranch, e)
		} else {
			a.addReloc(rJal, e)
		}
	case e.sym != "":
		return fmt.Errorf("%s: symbol %q not allowed here (use %%hi/%%lo or la)", in.op, e.sym)
	default:
		imm = e.add
	}
	w, err := encode(in, imm)
	if err != nil {
		return err
	}
	a.cur.data = binary.LittleEndian.AppendUint32(a.cur.data, w)
	a.cur.lines = append(a.cur.lines, entry{off: a.cur.size, size: 4, inst: true, op: in.op, st: a.st})
	a.cur.size += 4
	return nil
}

func (a *assembler) addReloc(typ uint32, e expr) {
	if e.sym == "." { // 現在位置はローカルラベルにして参照する
		e.sym = fmt.Sprintf(".Ldot%s%d", a.cur.name, a.cur.size)
		s := a.getSym(e.sym)
		s.defined, s.sec, s.value = true, a.cur.name, int64(a.cur.size)
	}
	a.getSym(e.sym) // 未定義なら未定義シンボルとして登録 (リンカが解決)
	a.cur.relocs = append(a.cur.relocs, reloc{off: a.cur.size, typ: typ, sym: e.sym, addend: e.add})
}

// emit はデータを追加する (.bss ではゼロ以外は書けない)。
func (a *assembler) emit(b []byte) error {
	if a.cur.nobits {
		for _, c := range b {
			if c != 0 {
				return fmt.Errorf("non-zero data in %s", a.cur.name)
			}
		}
	} else if a.final {
		a.cur.data = append(a.cur.data, b...)
	}
	if a.final && len(b) > 0 && a.st != nil {
		ls := a.cur.lines
		if n := len(ls); n > 0 && !ls[n-1].inst && ls[n-1].st == a.st && ls[n-1].off+ls[n-1].size == a.cur.size {
			ls[n-1].size += uint64(len(b)) // 同じ文の続き (.byte 1, 2, 3 など) はまとめる
		} else {
			a.cur.lines = append(ls, entry{off: a.cur.size, size: uint64(len(b)), st: a.st})
		}
	}
	a.cur.size += uint64(len(b))
	return nil
}

func (a *assembler) alignTo(n uint64) error {
	if n == 0 || n&(n-1) != 0 {
		return fmt.Errorf("alignment %d is not a power of two", n)
	}
	if n > a.cur.align {
		a.cur.align = n
	}
	pad := alignUp(a.cur.size, n) - a.cur.size
	if sectionKind(a.cur.name) == "text" && a.cur.size%4 == 0 {
		for ; pad > 0; pad -= 4 { // コード中の穴は nop で埋める
			a.emit([]byte{0x13, 0, 0, 0})
		}
	}
	return a.emit(make([]byte, pad))
}

func (a *assembler) directive(st stmt) error {
	args := st.args
	argName := func(k int) (string, error) {
		if k >= len(args) || len(args[k]) != 1 || args[k][0].kind != tIdent {
			return "", fmt.Errorf("%s: expected a name", st.name)
		}
		return args[k][0].text, nil
	}
	constArg := func(k int) (int64, error) {
		if k >= len(args) {
			return 0, fmt.Errorf("%s: missing operand", st.name)
		}
		e, err := parseExpr(args[k], a.lookup)
		if err == nil && (e.sym != "" || e.mod != "") {
			err = fmt.Errorf("%s: operand must be a constant", st.name)
		}
		return e.add, err
	}

	switch st.name {
	case ".text", ".data", ".bss", ".rodata":
		a.switchSection(st.name)
	case ".section":
		n, err := argName(0) // フラグ等の追加引数は無視
		if err != nil {
			return err
		}
		if sectionKind(n) == "" {
			return fmt.Errorf("unsupported section %q", n)
		}
		a.switchSection(n)

	case ".globl", ".global", ".weak":
		for k := range args {
			n, err := argName(k)
			if err != nil {
				return err
			}
			a.getSym(n).global = true
		}
	case ".equ", ".set":
		n, err := argName(0)
		if err != nil {
			return err
		}
		v, err := constArg(1)
		if err != nil {
			return err
		}
		a.equ[n] = v
		s := a.getSym(n)
		s.defined, s.abs, s.value = true, true, v

	case ".align", ".p2align", ".balign":
		v, err := constArg(0)
		if err != nil {
			return err
		}
		if st.name != ".balign" { // RISC-V の .align は 2 の冪指定
			v = 1 << v
		}
		return a.alignTo(uint64(v))

	case ".byte", ".half", ".short", ".2byte", ".word", ".long", ".4byte", ".dword", ".quad", ".8byte":
		size := map[string]int{".byte": 1, ".half": 2, ".short": 2, ".2byte": 2,
			".word": 4, ".long": 4, ".4byte": 4, ".dword": 8, ".quad": 8, ".8byte": 8}[st.name]
		for _, arg := range args {
			e, err := parseExpr(arg, a.lookup)
			if err != nil {
				return err
			}
			if e.mod != "" {
				return fmt.Errorf("%s: modifiers not allowed", st.name)
			}
			if e.sym != "" { // アドレスを埋め込む: リンカに絶対アドレスを書いてもらう
				if size < 4 {
					return fmt.Errorf("%s: symbol needs .word or .dword", st.name)
				}
				if a.final {
					a.addReloc(map[int]uint32{4: r32, 8: r64}[size], e)
				}
				e.add = 0
			}
			buf := binary.LittleEndian.AppendUint64(nil, uint64(e.add))
			if err := a.emit(buf[:size]); err != nil {
				return err
			}
		}

	case ".ascii", ".asciz", ".string":
		for _, arg := range args {
			if len(arg) != 1 || arg[0].kind != tStr {
				return fmt.Errorf("%s: expected string", st.name)
			}
			b := []byte(arg[0].text)
			if st.name != ".ascii" {
				b = append(b, 0)
			}
			if err := a.emit(b); err != nil {
				return err
			}
		}

	case ".zero", ".space", ".skip":
		n, err := constArg(0)
		if err != nil {
			return err
		}
		fill := int64(0)
		if len(args) > 1 {
			if fill, err = constArg(1); err != nil {
				return err
			}
		}
		b := make([]byte, n)
		for k := range b {
			b[k] = byte(fill)
		}
		return a.emit(b)

	case ".type", ".size", ".file", ".ident", ".option", ".attribute", ".local", ".cfi_startproc", ".cfi_endproc":
		// コンパイラが出しがちだが、この実装では意味を持たないもの
	default:
		return fmt.Errorf("unknown directive %s", st.name)
	}
	return nil
}

// sectionKind はセクション名を 4 種類 (text/rodata/data/bss) に分類する。
func sectionKind(name string) string {
	for _, k := range []struct{ prefix, kind string }{
		{".text", "text"}, {".rodata", "rodata"}, {".srodata", "rodata"},
		{".data", "data"}, {".sdata", "data"}, {".bss", "bss"}, {".sbss", "bss"},
	} {
		if name == k.prefix || strings.HasPrefix(name, k.prefix+".") {
			return k.kind
		}
	}
	return ""
}
