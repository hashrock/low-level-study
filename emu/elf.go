package main

import (
	"debug/elf"
	"fmt"
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

// ReadELF はファイルを開いて RISC-V 64 ビット ELF か確認し、ロード範囲を求める。
func ReadELF(path string) (*elf.File, *LoadedELF, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, nil, err
	}
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_RISCV {
		f.Close()
		return nil, nil, fmt.Errorf("%s: not a RISC-V 64-bit ELF (class=%v machine=%v)", path, f.Class, f.Machine)
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
		f.Close()
		return nil, nil, fmt.Errorf("%s: no PT_LOAD segments", path)
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
