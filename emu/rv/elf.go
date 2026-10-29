package rv

import (
	"bytes"
	"debug/elf"
	"fmt"
	"sort"
)

// ELF ローダ。ヘッダの解析は標準ライブラリ debug/elf に任せ、
// 「PT_LOAD セグメントをメモリにコピーする」部分だけを自前で書く。
//
// ELF ファイルの中身 (実行に必要な部分だけ):
//   ELF ヘッダ      : e_entry (開始アドレス), e_phoff (プログラムヘッダの位置)
//   プログラムヘッダ: セグメントごとに
//       p_type   = PT_LOAD ならメモリに載せる
//       p_offset = ファイル内の位置, p_filesz = ファイル上のサイズ
//       p_vaddr / p_paddr = 載せるアドレス, p_memsz = メモリ上のサイズ
//       (memsz > filesz の残りは 0 で埋める = .bss)

type LoadedELF struct {
	Entry   uint64
	MinAddr uint64 // 最も低いセグメントの先頭
	MaxAddr uint64 // 最も高いセグメントの末尾
	Phdr    uint64 // プログラムヘッダがメモリ上にあればそのアドレス (auxv の AT_PHDR 用)
	Phnum   uint64
}

// ParseELF はメモリ上の ELF を解析して RISC-V 64 ビット ELF か確認し、ロード範囲を求める。
func ParseELF(data []byte) (*elf.File, *LoadedELF, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_RISCV {
		return nil, nil, fmt.Errorf("not a RISC-V 64-bit ELF (class=%v machine=%v)", f.Class, f.Machine)
	}
	info := &LoadedELF{Entry: f.Entry, MinAddr: ^uint64(0), Phnum: uint64(len(f.Progs))}
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		info.MinAddr = min(info.MinAddr, p.Paddr)
		info.MaxAddr = max(info.MaxAddr, p.Paddr+p.Memsz)
	}
	if info.MaxAddr == 0 {
		return nil, nil, fmt.Errorf("no PT_LOAD segments")
	}
	return f, info, nil
}

// LoadELF は PT_LOAD セグメントを bus の RAM にコピーする。
// (仮想メモリが無いので p_paddr に載せる。普通の実行ファイルでは p_vaddr と同じ)
func LoadELF(f *elf.File, info *LoadedELF, bus *Bus) error {
	var phoff uint64 = 64 // ELF64 ヘッダの直後に置かれるのが普通
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		data := make([]byte, p.Memsz) // filesz 以降は 0 (bss)
		if p.Filesz > 0 {             // .bss だけのセグメントは filesz=0 (読むと EOF になる)
			if _, err := p.ReadAt(data[:p.Filesz], 0); err != nil {
				return fmt.Errorf("reading segment at 0x%x: %w", p.Paddr, err)
			}
		}
		if !bus.WriteBytes(p.Paddr, data) {
			return fmt.Errorf("segment 0x%x-0x%x does not fit in RAM (0x%x-0x%x)",
				p.Paddr, p.Paddr+p.Memsz, bus.RAMBase, bus.RAMBase+uint64(len(bus.RAM)))
		}
		if p.Off <= phoff && phoff < p.Off+p.Filesz {
			info.Phdr = p.Vaddr + phoff - p.Off
		}
	}
	return nil
}

// Symbol は ELF のシンボル (関数・ラベル)。逆アセンブル表示で使う。
type Symbol struct {
	Name string
	Addr uint64
	Size uint64
}

// Symbols はアドレス順に並んだシンボル表。
type Symbols []Symbol

// readSymbols は .symtab から名前付きの関数・オブジェクト・ラベルを集める (無ければ nil)。
func readSymbols(f *elf.File) Symbols {
	syms, err := f.Symbols()
	if err != nil {
		return nil
	}
	var out Symbols
	for _, s := range syms {
		t := elf.ST_TYPE(s.Info)
		if s.Name == "" || s.Value == 0 || s.Section == elf.SHN_UNDEF || s.Section == elf.SHN_ABS {
			continue
		}
		if t != elf.STT_FUNC && t != elf.STT_NOTYPE && t != elf.STT_OBJECT {
			continue
		}
		if s.Name[0] == '.' || s.Name[0] == '$' { // ローカルラベルやマッピングシンボル
			continue
		}
		out = append(out, Symbol{s.Name, s.Value, s.Size})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

// Lookup は addr を含む (または addr 以下で最も近い) シンボルとそこからのオフセットを返す。
func (s Symbols) Lookup(addr uint64) (Symbol, uint64, bool) {
	i := sort.Search(len(s), func(i int) bool { return s[i].Addr > addr }) - 1
	if i < 0 {
		return Symbol{}, 0, false
	}
	sym := s[i]
	if sym.Size > 0 && addr >= sym.Addr+sym.Size {
		return Symbol{}, 0, false
	}
	return sym, addr - sym.Addr, true
}

// Find は名前でシンボルを探す。
func (s Symbols) Find(name string) (Symbol, bool) {
	for _, sym := range s {
		if sym.Name == name {
			return sym, true
		}
	}
	return Symbol{}, false
}
