package minidb

import (
	"encoding/binary"
	"fmt"
	"strconv"
)

// 値と行 (レコード) のバイト列表現。

type ColType byte

const (
	TypeInt  ColType = 1
	TypeText ColType = 2
)

func (t ColType) String() string {
	if t == TypeInt {
		return "INTEGER"
	}
	return "TEXT"
}

type Kind byte

const (
	KindNull Kind = iota
	KindInt
	KindText
)

// Value は 1 つのセルの値。Kind によって Int か Str のどちらかを使う。
type Value struct {
	Kind Kind
	Int  int64
	Str  string
}

func IntValue(n int64) Value   { return Value{Kind: KindInt, Int: n} }
func TextValue(s string) Value { return Value{Kind: KindText, Str: s} }

func (v Value) String() string {
	switch v.Kind {
	case KindInt:
		return strconv.FormatInt(v.Int, 10)
	case KindText:
		return v.Str
	}
	return "NULL"
}

// compare は同じ型の非 NULL 値を比べて -1/0/1 を返す。
func compare(a, b Value) (int, error) {
	if a.Kind != b.Kind {
		return 0, fmt.Errorf("cannot compare %s with %s", a, b)
	}
	switch {
	case a.Kind == KindInt && a.Int < b.Int, a.Kind == KindText && a.Str < b.Str:
		return -1, nil
	case a.Kind == KindInt && a.Int > b.Int, a.Kind == KindText && a.Str > b.Str:
		return 1, nil
	}
	return 0, nil
}

// encodeRow: 各カラムを [型タグ 1byte][本体] で順に並べる。
//
//	NULL: tag=0
//	INT : tag=1, int64 (8 bytes, little endian)
//	TEXT: tag=2, uint16 長さ + UTF-8 バイト列
func encodeRow(vals []Value) []byte {
	var buf []byte
	for _, v := range vals {
		buf = append(buf, byte(v.Kind))
		switch v.Kind {
		case KindInt:
			buf = binary.LittleEndian.AppendUint64(buf, uint64(v.Int))
		case KindText:
			buf = binary.LittleEndian.AppendUint16(buf, uint16(len(v.Str)))
			buf = append(buf, v.Str...)
		}
	}
	return buf
}

func decodeRow(buf []byte, ncols int) ([]Value, error) {
	vals := make([]Value, ncols)
	off := 0
	for i := range vals {
		if off >= len(buf) {
			return nil, fmt.Errorf("corrupt row")
		}
		kind := Kind(buf[off])
		off++
		switch kind {
		case KindNull:
		case KindInt:
			vals[i] = IntValue(int64(binary.LittleEndian.Uint64(buf[off:])))
			off += 8
		case KindText:
			n := int(binary.LittleEndian.Uint16(buf[off:]))
			off += 2
			vals[i] = TextValue(string(buf[off : off+n]))
			off += n
		default:
			return nil, fmt.Errorf("corrupt row: bad tag %d", kind)
		}
	}
	return vals, nil
}
