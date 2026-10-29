package rvasm

// elf.go — ELF64 ファイルを書き出す共通ルーチン。
// オブジェクトファイル (ET_REL) と実行ファイル (ET_EXEC) の両方で使う。
// ヘッダの構造体は標準ライブラリ debug/elf のもの (Header64 等) をそのまま使う。
//
// ファイルの並び:
//
//	ELF ヘッダ (64B) | プログラムヘッダ × n (56B each) | 各セクションの中身 ... |
//	.shstrtab | セクションヘッダ表 (64B each)

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
)

// RISC-V のリロケーション種別 (psABI より)
const (
	rNone       = 0
	r32         = 1  // 絶対アドレス 32bit (.word sym)
	r64         = 2  // 絶対アドレス 64bit (.dword sym)
	rBranch     = 16 // B 形式 PC 相対
	rJal        = 17 // J 形式 PC 相対
	rCall       = 18 // auipc+jalr ペア
	rCallPLT    = 19 // 同上 (静的リンクでは CALL と同じ)
	rPcrelHi20  = 23 // auipc の上位 20bit (PC 相対)
	rPcrelLo12I = 24 // 対応する auipc の下位 12bit (I 形式)
	rPcrelLo12S = 25 // 同上 (S 形式)
	rHi20       = 26 // lui の上位 20bit (絶対)
	rLo12I      = 27 // 下位 12bit (I 形式, 絶対)
	rLo12S      = 28 // 下位 12bit (S 形式, 絶対)
	rRelax      = 51 // リンカ緩和のヒント (本実装では無視)
)

var relocNames = map[uint32]string{
	r32: "R_RISCV_32", r64: "R_RISCV_64", rBranch: "R_RISCV_BRANCH", rJal: "R_RISCV_JAL",
	rCall: "R_RISCV_CALL", rCallPLT: "R_RISCV_CALL_PLT", rPcrelHi20: "R_RISCV_PCREL_HI20",
	rPcrelLo12I: "R_RISCV_PCREL_LO12_I", rPcrelLo12S: "R_RISCV_PCREL_LO12_S",
	rHi20: "R_RISCV_HI20", rLo12I: "R_RISCV_LO12_I", rLo12S: "R_RISCV_LO12_S",
}

const ehdrSize, phdrSize, shdrSize = 64, 56, 64

type elfSection struct {
	name    string
	typ     elf.SectionType
	flags   elf.SectionFlag
	addr    uint64
	offset  uint64 // 0 なら自動配置 (align に従う)
	data    []byte
	size    uint64 // SHT_NOBITS (.bss) のサイズ
	link    uint32
	info    uint32
	align   uint64
	entsize uint64
}

// writeELF は ELF ファイル全体のバイト列を作る。
// secs[k] のセクション番号は k+1 (0 番は NULL セクション) 。
func writeELF(etype elf.Type, entry uint64, phdrs []elf.Prog64, secs []*elfSection) []byte {
	var shstr bytes.Buffer
	shstr.WriteByte(0)
	nameOff := func(s string) uint32 {
		off := uint32(shstr.Len())
		shstr.WriteString(s + "\x00")
		return off
	}

	var body bytes.Buffer
	cur := uint64(ehdrSize + phdrSize*len(phdrs))
	shdrs := []elf.Section64{{}} // NULL セクション
	for _, s := range secs {
		off := cur
		if s.offset != 0 {
			off = s.offset
		} else if s.align > 1 && s.typ != elf.SHT_NOBITS {
			off = alignUp(cur, s.align)
		}
		size := uint64(len(s.data))
		if s.typ == elf.SHT_NOBITS {
			size = s.size
		} else {
			body.Write(make([]byte, off-cur))
			body.Write(s.data)
			cur = off + uint64(len(s.data))
		}
		shdrs = append(shdrs, elf.Section64{
			Name: nameOff(s.name), Type: uint32(s.typ), Flags: uint64(s.flags),
			Addr: s.addr, Off: off, Size: size, Link: s.link, Info: s.info,
			Addralign: s.align, Entsize: s.entsize,
		})
	}
	// 最後に .shstrtab (セクション名の文字列表)
	shstrIdx := len(shdrs)
	name := nameOff(".shstrtab")
	shdrs = append(shdrs, elf.Section64{Name: name, Type: uint32(elf.SHT_STRTAB), Off: cur, Size: uint64(shstr.Len()), Addralign: 1})
	body.Write(shstr.Bytes())
	cur += uint64(shstr.Len())
	shoff := alignUp(cur, 8)
	body.Write(make([]byte, shoff-cur))

	hdr := elf.Header64{
		Type: uint16(etype), Machine: uint16(elf.EM_RISCV), Version: uint32(elf.EV_CURRENT),
		Entry: entry, Shoff: shoff, Ehsize: ehdrSize,
		Shentsize: shdrSize, Shnum: uint16(len(shdrs)), Shstrndx: uint16(shstrIdx),
		Flags: 0, // float ABI = soft, RVC なし
	}
	copy(hdr.Ident[:], "\x7fELF")
	hdr.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	hdr.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	hdr.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	hdr.Ident[elf.EI_OSABI] = byte(elf.ELFOSABI_NONE)
	if len(phdrs) > 0 {
		hdr.Phoff, hdr.Phentsize, hdr.Phnum = ehdrSize, phdrSize, uint16(len(phdrs))
	}

	var out bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&out, le, hdr)
	binary.Write(&out, le, phdrs)
	out.Write(body.Bytes())
	binary.Write(&out, le, shdrs)
	return out.Bytes()
}

// elfSym は .symtab に書くシンボル 1 つ。
type elfSym struct {
	name  string
	value uint64
	bind  elf.SymBind
	typ   elf.SymType
	shndx uint16
}

// buildSymtab は .symtab と .strtab の中身を作る。
// ELF の決まりで、ローカルシンボルはグローバルより前に並べること (呼び出し側の責任) 。
func buildSymtab(syms []elfSym) (symtab, strtab []byte) {
	var st bytes.Buffer
	str := []byte{0}
	binary.Write(&st, binary.LittleEndian, elf.Sym64{}) // 0 番は空シンボル
	for _, s := range syms {
		e := elf.Sym64{
			Info:  elf.ST_INFO(s.bind, s.typ),
			Shndx: s.shndx,
			Value: s.value,
		}
		if s.name != "" {
			e.Name = uint32(len(str))
			str = append(append(str, s.name...), 0)
		}
		binary.Write(&st, binary.LittleEndian, e)
	}
	return st.Bytes(), str
}

func alignUp(v, a uint64) uint64 {
	if a <= 1 {
		return v
	}
	return (v + a - 1) &^ (a - 1)
}
