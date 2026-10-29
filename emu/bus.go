package main

import "encoding/binary"

// バス: 物理アドレスを RAM か各デバイスに振り分ける。
// (MMU/ページングは無いので、CPU のアドレスがそのまま物理アドレス)

// Device はメモリマップド I/O デバイス。off はデバイス先頭からのオフセット。
type Device interface {
	Load(off uint64, size int) uint64
	Store(off uint64, size int, v uint64)
}

type mapping struct {
	base, size uint64
	dev        Device
}

type Bus struct {
	RAMBase uint64
	RAM     []byte
	devices []mapping
}

func NewBus(ramBase, ramSize uint64) *Bus {
	return &Bus{RAMBase: ramBase, RAM: make([]byte, ramSize)}
}

func (b *Bus) Map(base, size uint64, dev Device) {
	b.devices = append(b.devices, mapping{base, size, dev})
}

// ram は addr から size バイトが RAM に収まっていればそのスライスを返す。
func (b *Bus) ram(addr uint64, size int) []byte {
	off := addr - b.RAMBase // addr < RAMBase ならラップして巨大になり、下の判定で弾かれる
	if off < uint64(len(b.RAM)) && off+uint64(size) <= uint64(len(b.RAM)) {
		return b.RAM[off : off+uint64(size)]
	}
	return nil
}

func (b *Bus) device(addr uint64) (Device, uint64) {
	for _, m := range b.devices {
		if addr >= m.base && addr-m.base < m.size {
			return m.dev, addr - m.base
		}
	}
	return nil, 0
}

// Load は size (1/2/4/8) バイトをリトルエンディアンで読む。
func (b *Bus) Load(addr uint64, size int) (uint64, bool) {
	if m := b.ram(addr, size); m != nil {
		switch size {
		case 1:
			return uint64(m[0]), true
		case 2:
			return uint64(binary.LittleEndian.Uint16(m)), true
		case 4:
			return uint64(binary.LittleEndian.Uint32(m)), true
		default:
			return binary.LittleEndian.Uint64(m), true
		}
	}
	if d, off := b.device(addr); d != nil {
		return d.Load(off, size), true
	}
	return 0, false
}

func (b *Bus) Store(addr uint64, size int, v uint64) bool {
	if m := b.ram(addr, size); m != nil {
		switch size {
		case 1:
			m[0] = byte(v)
		case 2:
			binary.LittleEndian.PutUint16(m, uint16(v))
		case 4:
			binary.LittleEndian.PutUint32(m, uint32(v))
		default:
			binary.LittleEndian.PutUint64(m, v)
		}
		return true
	}
	if d, off := b.device(addr); d != nil {
		d.Store(off, size, v)
		return true
	}
	return false
}

// ReadBytes / WriteBytes はホスト側 (ローダやシステムコール) から RAM を触るためのもの。
func (b *Bus) ReadBytes(addr, n uint64) ([]byte, bool) {
	m := b.ram(addr, int(n))
	return m, m != nil || n == 0
}

func (b *Bus) WriteBytes(addr uint64, data []byte) bool {
	m := b.ram(addr, len(data))
	if m == nil && len(data) > 0 {
		return false
	}
	copy(m, data)
	return true
}

// regRead / regWrite: 64 ビットレジスタの一部 (4 バイト等) へのアクセスを扱うヘルパ。
// 32 ビット OS は mtime を 2 回の 4 バイトアクセスで読むので、それに対応する。
func regRead(reg uint64, off uint64, size int) uint64 {
	v := reg >> (8 * (off & 7))
	if size < 8 {
		v &= 1<<(8*size) - 1
	}
	return v
}

func regWrite(reg uint64, off uint64, size int, v uint64) uint64 {
	shift := 8 * (off & 7)
	mask := uint64(1)<<(8*size) - 1
	if size == 8 {
		mask = ^uint64(0)
	}
	return reg&^(mask<<shift) | (v&mask)<<shift
}

// TestDevice は QEMU virt の "sifive_test" (0x100000)。書き込むと電源断/リセットする。
type TestDevice struct{ cpu *CPU }

func (t *TestDevice) Load(off uint64, size int) uint64 { return 0 }
func (t *TestDevice) Store(off uint64, size int, v uint64) {
	switch v & 0xffff {
	case 0x5555: // PASS (電源断)
		t.cpu.Halt(0, nil)
	case 0x3333: // FAIL: 上位 16 ビットが終了コード
		t.cpu.Halt(int(v>>16), nil)
	case 0x7777: // リセット (このエミュレータでは終了として扱う)
		t.cpu.Halt(0, nil)
	}
}
