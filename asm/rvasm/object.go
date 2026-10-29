package rvasm

// object.go — オブジェクトファイル (ELF64 リロケータブル, ET_REL) の読み書き。
//
// 本物の ELF .o 形式を使うので、binutils の readelf/objdump でも中身を確認できる。
// 書き出すセクション:
//
//	.text / .data / .rodata / .bss  … アセンブル結果の中身
//	.rela.text など                 … リロケーション (Elf64_Rela の配列)
//	.symtab / .strtab               … シンボル表とその名前
//	.shstrtab                       … セクション名 (elf.go が自動で追加)

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
)

func sectionFlags(name string) elf.SectionFlag {
	switch sectionKind(name) {
	case "text":
		return elf.SHF_ALLOC | elf.SHF_EXECINSTR
	case "rodata":
		return elf.SHF_ALLOC
	}
	return elf.SHF_ALLOC | elf.SHF_WRITE
}

// writeObject はオブジェクトを ELF .o のバイト列にする。
func writeObject(o *object) ([]byte, error) {
	var secs []*elfSection
	secIndex := map[string]uint16{}
	for _, s := range o.sections {
		es := &elfSection{name: s.name, typ: elf.SHT_PROGBITS, flags: sectionFlags(s.name), data: s.data, align: s.align}
		if s.nobits {
			es.typ, es.data, es.size = elf.SHT_NOBITS, nil, s.size
		}
		secs = append(secs, es)
		secIndex[s.name] = uint16(len(secs)) // 0 番は NULL
	}

	// シンボル表: ローカル → グローバルの順。未定義シンボルはグローバル扱い。
	var locals, globals []elfSym
	for _, s := range o.symbols {
		es := elfSym{name: s.name, value: uint64(s.value), bind: elf.STB_LOCAL, typ: elf.STT_NOTYPE}
		switch {
		case s.abs:
			es.shndx = uint16(elf.SHN_ABS)
		case s.defined:
			es.shndx = secIndex[s.sec]
		}
		if s.global || !s.defined {
			es.bind = elf.STB_GLOBAL
			globals = append(globals, es)
		} else {
			locals = append(locals, es)
		}
	}
	all := append(locals, globals...)
	symIndex := map[string]uint32{}
	for k, s := range all {
		symIndex[s.name] = uint32(k + 1)
	}
	symtabIdx := uint32(len(secs) + countRela(o) + 1)

	// .rela.<name> セクション
	for _, s := range o.sections {
		if len(s.relocs) == 0 {
			continue
		}
		var buf bytes.Buffer
		for _, r := range s.relocs {
			binary.Write(&buf, binary.LittleEndian, elf.Rela64{
				Off: r.off, Info: elf.R_INFO(symIndex[r.sym], r.typ), Addend: r.addend,
			})
		}
		secs = append(secs, &elfSection{
			name: ".rela" + s.name, typ: elf.SHT_RELA, flags: elf.SHF_INFO_LINK, data: buf.Bytes(),
			link: symtabIdx, info: uint32(secIndex[s.name]), align: 8, entsize: 24,
		})
	}
	symtab, strtab := buildSymtab(all)
	secs = append(secs,
		&elfSection{name: ".symtab", typ: elf.SHT_SYMTAB, data: symtab, link: symtabIdx + 1,
			info: uint32(len(locals) + 1), align: 8, entsize: 24},
		&elfSection{name: ".strtab", typ: elf.SHT_STRTAB, data: strtab, align: 1},
	)
	return writeELF(elf.ET_REL, 0, nil, secs), nil
}

func countRela(o *object) int {
	n := 0
	for _, s := range o.sections {
		if len(s.relocs) > 0 {
			n++
		}
	}
	return n
}

// readObject は ELF .o を読み込んで object に戻す (リンカの入力)。
func readObject(name string, data []byte) (*object, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	if f.Type != elf.ET_REL || f.Machine != elf.EM_RISCV || f.Class != elf.ELFCLASS64 {
		return nil, fmt.Errorf("%s: not a RISC-V 64-bit relocatable object", name)
	}
	o := &object{name: name}
	bySecIdx := map[int]*section{}
	for k, s := range f.Sections {
		if s.Flags&elf.SHF_ALLOC == 0 || sectionKind(s.Name) == "" {
			continue // デバッグ情報などは無視
		}
		sec := &section{name: s.Name, align: s.Addralign, size: s.Size, nobits: s.Type == elf.SHT_NOBITS}
		if !sec.nobits {
			if sec.data, err = s.Data(); err != nil {
				return nil, err
			}
		}
		if sec.align == 0 {
			sec.align = 1
		}
		o.sections = append(o.sections, sec)
		bySecIdx[k] = sec
	}

	syms, err := f.Symbols() // syms[k] がシンボル表の k+1 番
	if err != nil && err != elf.ErrNoSymbols {
		return nil, err
	}
	names := make([]string, len(syms)+1)
	for k, s := range syms {
		sym := &symbol{name: s.Name, value: int64(s.Value), global: elf.ST_BIND(s.Info) != elf.STB_LOCAL}
		switch {
		case s.Section == elf.SHN_UNDEF:
		case s.Section == elf.SHN_ABS:
			sym.defined, sym.abs = true, true
		case bySecIdx[int(s.Section)] != nil:
			sym.defined, sym.sec = true, bySecIdx[int(s.Section)].name
		default:
			continue // 対象外セクションのシンボル
		}
		if elf.ST_TYPE(s.Info) == elf.STT_SECTION {
			sym.name = sym.sec // セクションシンボル (他のアセンブラが使う) はセクション名で呼ぶ
		}
		names[k+1] = sym.name
		o.symbols = append(o.symbols, sym)
	}

	for _, s := range f.Sections {
		target := bySecIdx[int(s.Info)]
		if s.Type != elf.SHT_RELA || target == nil {
			continue
		}
		d, err := s.Data()
		if err != nil {
			return nil, err
		}
		rels := make([]elf.Rela64, len(d)/24)
		binary.Read(bytes.NewReader(d), binary.LittleEndian, rels)
		for _, r := range rels {
			idx := elf.R_SYM64(r.Info)
			if int(idx) >= len(names) {
				return nil, fmt.Errorf("%s: bad symbol index %d", name, idx)
			}
			target.relocs = append(target.relocs, reloc{off: r.Off, typ: elf.R_TYPE64(r.Info), sym: names[idx], addend: r.Addend})
		}
	}
	return o, nil
}
