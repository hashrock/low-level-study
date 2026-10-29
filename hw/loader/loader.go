// Package loader は .s / .c をビルドして ELF にし、Verilog の RAM に読み込ませる
// $readmemh 形式の hex イメージ (プログラム + 初期スタック) を作る。
//
// ハードウェアには OS もローダも無いので、「exec が終わった瞬間のメモリ」をここで作って
// RAM の初期値として焼き込む。スタックの形 (argc / argv / envp / auxv) は ../emu の
// ユーザモード (rv/machine.go の setupStack) と同じにしてあり、lockstep 比較では
// emu 側にも同じスタックを書き込んで、両者の初期状態をぴったり揃える。
package loader

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rvasm/rvasm"
	"rvcc/rvcc"
)

// RAMSize は Verilog 側の RAM の大きさ (rtl/ram.v の WORDS * 8 と一致させる)。
const RAMSize = 1 << 20

// BuildFiles は .s (rvasm) または .c (rvcc, -O2, libc 込み) のファイル群から ELF を作る。
// .s と .c の混在はしない (どちらか一方)。
func BuildFiles(paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no input files")
	}
	isC := strings.HasSuffix(paths[0], ".c")
	var asmSrcs []rvasm.Source
	var cSrcs []rvcc.Source
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(p, ".c") != isC {
			return nil, fmt.Errorf("%s: cannot mix .c and .s", p)
		}
		if isC {
			cSrcs = append(cSrcs, rvcc.Source{Name: filepath.Base(p), Text: string(b)})
		} else {
			asmSrcs = append(asmSrcs, rvasm.Source{Name: filepath.Base(p), Text: string(b)})
		}
	}
	if isC {
		return rvcc.Build(cSrcs, 2)
	}
	return rvasm.Build(asmSrcs...)
}

// Image は RAM の初期内容と、CPU のリセット時の状態。
type Image struct {
	Mem      []byte // RAM 全体 (RAMSize バイト, 0 番地から)
	Entry    uint64 // リセット後の PC (ELF のエントリ)
	SP       uint64 // 初期スタックポインタ (x2)
	BrkStart uint64 // ヒープ (brk) の開始位置 = ELF の末尾をページ境界に切り上げたもの
	// スタック領域 [SP, RAMSize) — emu に同じ内容を書き込むため
	StackLo uint64
}

func pageUp(v uint64) uint64 { return (v + 4095) &^ 4095 }

// Load は ELF を RAM イメージに展開し、Linux と同じ形の初期スタックを積む。
func Load(elfData []byte, argv []string) (*Image, error) {
	f, err := elf.NewFile(bytes.NewReader(elfData))
	if err != nil {
		return nil, err
	}
	if f.Machine != elf.EM_RISCV || f.Class != elf.ELFCLASS64 {
		return nil, fmt.Errorf("not an RV64 ELF")
	}
	im := &Image{Mem: make([]byte, RAMSize), Entry: f.Entry}
	var maxAddr, phdr uint64
	const phoff = 64
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		if p.Paddr+p.Memsz > RAMSize/2 {
			return nil, fmt.Errorf("segment 0x%x-0x%x does not fit in the lower half of RAM", p.Paddr, p.Paddr+p.Memsz)
		}
		if p.Filesz > 0 {
			if _, err := p.ReadAt(im.Mem[p.Paddr:p.Paddr+p.Filesz], 0); err != nil {
				return nil, err
			}
		}
		maxAddr = max(maxAddr, p.Paddr+p.Memsz)
		if p.Off <= phoff && phoff < p.Off+p.Filesz {
			phdr = p.Vaddr + phoff - p.Off
		}
	}
	im.BrkStart = pageUp(maxAddr)

	// ---- 初期スタック (emu の setupStack と同じ手順) ----
	if len(argv) == 0 {
		argv = []string{"a.out"}
	}
	sp := uint64(RAMSize)
	push := func(data []byte) uint64 {
		sp -= uint64(len(data))
		copy(im.Mem[sp:], data)
		return sp
	}
	random := push([]byte("rvemu-random-16b"))
	var argvPtr []uint64
	for _, a := range argv {
		argvPtr = append(argvPtr, push(append([]byte(a), 0)))
	}
	words := []uint64{uint64(len(argv))}
	words = append(words, argvPtr...)
	words = append(words, 0, 0) // argv 終端, envp 終端
	words = append(words, 3, phdr, 4, 56, 5, uint64(len(f.Progs)), 6, 4096, 9, f.Entry, 25, random, 0, 0)
	sp -= uint64(8 * len(words))
	sp &^= 15
	for i, w := range words {
		binary.LittleEndian.PutUint64(im.Mem[sp+uint64(8*i):], w)
	}
	im.SP, im.StackLo = sp, sp
	return im, nil
}

// WriteHex は RAM イメージを $readmemh 形式 (1 行 = 64 ビットのワード) で書く。
// 0 のワードは省略し、"@番地" (ワード単位) で飛ばす。RAM は最初に 0 クリアされる。
func (im *Image) WriteHex(path string) error {
	var b strings.Builder
	next := -1
	for w := 0; w < len(im.Mem)/8; w++ {
		v := binary.LittleEndian.Uint64(im.Mem[w*8:])
		if v == 0 {
			continue
		}
		if w != next {
			fmt.Fprintf(&b, "@%x\n", w)
		}
		fmt.Fprintf(&b, "%016x\n", v)
		next = w + 1
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// PlusArgs はテストベンチに渡す +entry= +sp= +brk= を返す。
func (im *Image) PlusArgs() []string {
	return []string{
		fmt.Sprintf("+entry=%x", im.Entry),
		fmt.Sprintf("+sp=%x", im.SP),
		fmt.Sprintf("+brk=%x", im.BrkStart),
	}
}

// Symbols は ELF の関数シンボルをアドレス順に返す (make wave の PC 指定用)。
func Symbols(elfData []byte) map[string]uint64 {
	f, err := elf.NewFile(bytes.NewReader(elfData))
	if err != nil {
		return nil
	}
	syms, _ := f.Symbols()
	out := map[string]uint64{}
	for _, s := range syms {
		if s.Name != "" {
			out[s.Name] = s.Value
		}
	}
	return out
}
