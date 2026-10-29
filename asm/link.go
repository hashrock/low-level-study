package main

// link.go — 静的リンカ。複数の .o を 1 つの ELF 実行ファイルにまとめる。
//
//	1. 配置 (layout)   : 同じ種類のセクション (.text 同士など) を連結し、
//	                     出力ファイル上のオフセットと仮想アドレスを決める
//	2. シンボル解決    : 各シンボルの最終アドレスを計算する
//	                     (グローバルは全ファイル共通の表、ローカルはファイル内だけ)
//	3. リロケーション  : 命令やデータの「穴」にアドレス/オフセットを書き込む
//	4. 出力            : プログラムヘッダ付きの ELF (ET_EXEC) を書く
//
// メモリ配置 (base = 0x10000):
//
//	0x10000  [ELF ヘッダ + プログラムヘッダ][.text][.rodata]   R-X セグメント
//	次のページ [.data][.bss]                                     RW- セグメント

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"sort"
)

const (
	baseAddr = 0x10000
	pageSize = 0x1000
)

var outKinds = []string{"text", "rodata", "data", "bss"}

type outSection struct {
	kind  string
	data  []byte
	size  uint64
	align uint64
	addr  uint64
	off   uint64
}

// placement: 入力セクションが出力セクションのどこに置かれたか
type placement struct {
	out *outSection
	off uint64
}

func link(objs []*object) ([]byte, error) {
	outs := map[string]*outSection{}
	for _, k := range outKinds {
		outs[k] = &outSection{kind: k, align: 1}
	}

	// ---- 1. 配置: 入力セクションを種類ごとに連結 ----
	place := map[*section]placement{}
	for _, o := range objs {
		for _, s := range o.sections {
			out := outs[sectionKind(s.name)]
			off := alignUp(out.size, s.align)
			if s.align > out.align {
				out.align = s.align
			}
			if !s.nobits {
				out.data = append(out.data, make([]byte, off-out.size)...)
				out.data = append(out.data, s.data...)
			}
			out.size = off + s.size
			place[s] = placement{out, off}
		}
	}
	text, rodata, data, bss := outs["text"], outs["rodata"], outs["data"], outs["bss"]
	hasRW := data.size+bss.size > 0
	nph := 1
	if hasRW {
		nph = 2
	}
	// R-X セグメント: ファイル先頭 (ヘッダ含む) から .rodata の終わりまで
	text.off = alignUp(uint64(ehdrSize+phdrSize*nph), maxU(text.align, 16))
	rodata.off = alignUp(text.off+text.size, rodata.align)
	rxEnd := rodata.off + rodata.size
	// RW- セグメント: 次のページ境界から。ファイルオフセットと仮想アドレスを
	// ページ内で一致させる必要がある (mmap の制約) ので addr = base + off とする。
	data.off = alignUp(rxEnd, pageSize)
	bss.off = data.off + data.size // ファイル上は場所を取らない
	for _, s := range []*outSection{text, rodata, data} {
		s.addr = baseAddr + s.off
	}
	bss.addr = alignUp(data.addr+data.size, bss.align)

	// ---- 2. シンボル解決 ----
	symAddr := func(o *object, s *symbol) uint64 {
		if s.abs {
			return uint64(s.value)
		}
		p := place[o.section(s.sec)]
		return p.out.addr + p.off + uint64(s.value)
	}
	globals := map[string]uint64{}
	for _, o := range objs {
		for _, s := range o.symbols {
			if !s.global || !s.defined {
				continue
			}
			if _, dup := globals[s.name]; dup {
				return nil, fmt.Errorf("duplicate symbol %q (in %s)", s.name, o.name)
			}
			globals[s.name] = symAddr(o, s)
		}
	}
	resolve := func(o *object, name string) (uint64, error) {
		if s := o.symbol(name); s != nil && s.defined {
			return symAddr(o, s), nil
		}
		if v, ok := globals[name]; ok {
			return v, nil
		}
		return 0, fmt.Errorf("%s: undefined symbol %q", o.name, name)
	}

	// ---- 3. リロケーション適用 ----
	// %pcrel_lo は「auipc の位置」を指すので、先に auipc 側 (HI20) の
	// オフセットを位置ごとに記録しておく。
	type fixup struct {
		o   *object
		r   reloc
		p   uint64 // 書き込む位置 (仮想アドレス)
		buf []byte // 書き込む位置からのバイト列
		s   uint64 // シンボルのアドレス
	}
	var fixups []fixup
	pcrelHi := map[uint64]int64{}
	for _, o := range objs {
		for _, sec := range o.sections {
			pl := place[sec]
			for _, r := range sec.relocs {
				if r.typ == rRelax || r.typ == rNone {
					continue
				}
				if sec.nobits {
					return nil, fmt.Errorf("%s: relocation in %s", o.name, sec.name)
				}
				s, err := resolve(o, r.sym)
				if err != nil {
					return nil, err
				}
				p := pl.out.addr + pl.off + r.off
				fx := fixup{o, r, p, pl.out.data[pl.off+r.off:], s}
				if r.typ == rPcrelHi20 {
					pcrelHi[p] = int64(s) + r.addend - int64(p)
				}
				fixups = append(fixups, fx)
			}
		}
	}
	for _, fx := range fixups {
		if err := applyReloc(fx.r.typ, fx.buf, int64(fx.s)+fx.r.addend, int64(fx.p), pcrelHi); err != nil {
			return nil, fmt.Errorf("%s: %s against %q: %v", fx.o.name, relocNames[fx.r.typ], fx.r.sym, err)
		}
	}

	entry, ok := globals["_start"]
	if !ok {
		return nil, fmt.Errorf("entry symbol _start not found (did you forget .globl _start?)")
	}

	// ---- 4. 出力 ----
	phdrs := []elf.Prog64{{
		Type: uint32(elf.PT_LOAD), Flags: uint32(elf.PF_R | elf.PF_X),
		Off: 0, Vaddr: baseAddr, Paddr: baseAddr, Filesz: rxEnd, Memsz: rxEnd, Align: pageSize,
	}}
	if hasRW {
		phdrs = append(phdrs, elf.Prog64{
			Type: uint32(elf.PT_LOAD), Flags: uint32(elf.PF_R | elf.PF_W),
			Off: data.off, Vaddr: data.addr, Paddr: data.addr,
			Filesz: data.size, Memsz: bss.addr + bss.size - data.addr, Align: pageSize,
		})
	}
	var secs []*elfSection
	secIdx := map[string]uint16{}
	names := map[string]string{"text": ".text", "rodata": ".rodata", "data": ".data", "bss": ".bss"}
	for _, k := range outKinds {
		s := outs[k]
		if s.size == 0 {
			continue
		}
		es := &elfSection{name: names[k], typ: elf.SHT_PROGBITS, flags: sectionFlags(names[k]),
			addr: s.addr, offset: s.off, data: s.data, align: s.align}
		if k == "bss" {
			es.typ, es.data, es.size, es.offset = elf.SHT_NOBITS, nil, s.size, 0
		}
		secs = append(secs, es)
		secIdx[k] = uint16(len(secs))
	}
	// デバッグ用に .symtab も付けておく (エミュレータや objdump で役立つ)
	var locals, globs []elfSym
	for _, o := range objs {
		for _, s := range o.symbols {
			if !s.defined || (s.sec != "" && s.name == s.sec) {
				continue
			}
			es := elfSym{name: s.name, value: symAddr(o, s), bind: elf.STB_LOCAL, shndx: uint16(elf.SHN_ABS)}
			if !s.abs {
				es.shndx = secIdx[sectionKind(s.sec)]
			}
			if s.global {
				es.bind = elf.STB_GLOBAL
				globs = append(globs, es)
			} else {
				locals = append(locals, es)
			}
		}
	}
	sort.SliceStable(globs, func(i, j int) bool { return globs[i].value < globs[j].value })
	symtab, strtab := buildSymtab(append(locals, globs...))
	n := uint32(len(secs))
	secs = append(secs,
		&elfSection{name: ".symtab", typ: elf.SHT_SYMTAB, data: symtab, link: n + 2,
			info: uint32(len(locals) + 1), align: 8, entsize: 24},
		&elfSection{name: ".strtab", typ: elf.SHT_STRTAB, data: strtab, align: 1})
	return writeELF(elf.ET_EXEC, entry, phdrs, secs), nil
}

// applyReloc は 1 つのリロケーションを適用する。
// v = S + A (シンボルのアドレス + 加数), p = P (書き込む位置のアドレス)
func applyReloc(typ uint32, buf []byte, v, p int64, pcrelHi map[uint64]int64) error {
	le := binary.LittleEndian
	patch := func(at int, f format, imm int64) error {
		w, err := patchImm(le.Uint32(buf[at:]), f, imm)
		if err == nil {
			le.PutUint32(buf[at:], w)
		}
		return err
	}
	fits32 := func(x int64) error {
		if x != int64(int32(x)) {
			return fmt.Errorf("offset %#x does not fit in 32 bits", x)
		}
		return nil
	}
	switch typ {
	case r32:
		le.PutUint32(buf, uint32(v))
	case r64:
		le.PutUint64(buf, uint64(v))
	case rBranch:
		return patch(0, fB, v-p)
	case rJal:
		return patch(0, fJ, v-p)
	case rCall, rCallPLT: // auipc (P) + jalr (P+4)
		off := v - p
		if err := fits32(off); err != nil {
			return err
		}
		if err := patch(0, fU, hi20(off)); err != nil {
			return err
		}
		return patch(4, fI, lo12(off))
	case rPcrelHi20:
		off := v - p
		if err := fits32(off); err != nil {
			return err
		}
		return patch(0, fU, hi20(off))
	case rPcrelLo12I, rPcrelLo12S:
		// v は auipc 命令の位置。そこで計算したオフセットの下位 12bit を使う。
		off, ok := pcrelHi[uint64(v)]
		if !ok {
			return fmt.Errorf("no matching R_RISCV_PCREL_HI20 at %#x", v)
		}
		if typ == rPcrelLo12S {
			return patch(0, fS, lo12(off))
		}
		return patch(0, fI, lo12(off))
	case rHi20:
		if err := fits32(v); err != nil {
			return err
		}
		return patch(0, fU, hi20(v))
	case rLo12I:
		return patch(0, fI, lo12(v))
	case rLo12S:
		return patch(0, fS, lo12(v))
	default:
		return fmt.Errorf("unsupported relocation type %d", typ)
	}
	return nil
}

func maxU(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
