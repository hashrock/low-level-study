package minilang

import (
	"math"
	"strconv"
	"strings"
)

// Value は実行時の値。実際の型は次のいずれか:
//
//	nil, bool, float64, string, *Array, *Map, *Closure, *Builtin
type Value = any

type Array struct{ Elems []Value }

// Map は挿入順を保つ文字列キーのマップ。
type Map struct {
	Keys []string
	M    map[string]Value
}

func NewMap() *Map { return &Map{M: map[string]Value{}} }

func (m *Map) Set(k string, v Value) {
	if _, ok := m.M[k]; !ok {
		m.Keys = append(m.Keys, k)
	}
	m.M[k] = v
}

// Cell は変数 1 個分の箱。クロージャはこれをポインタで共有する。
type Cell struct{ V Value }

type Closure struct {
	P    *Proto
	Free []*Cell
}

type Builtin struct {
	Name string
	Fn   func(vm *VM, args []Value) (Value, error)
}

func typeName(v Value) string {
	switch v.(type) {
	case nil:
		return "nil"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case *Array:
		return "array"
	case *Map:
		return "map"
	case *Closure, *Builtin:
		return "function"
	}
	return "unknown"
}

// truthy: false と nil だけが偽。0 や "" は真。
func truthy(v Value) bool { return v != nil && v != false }

func formatNumber(n float64) string {
	if n == math.Trunc(n) && math.Abs(n) < 1e15 {
		return strconv.FormatInt(int64(n), 10)
	}
	return strconv.FormatFloat(n, 'g', -1, 64)
}

// Format は値を表示用の文字列にする (print と同じ規則)。
func Format(v Value, quote bool) string { return format(v, quote) }

// TypeName は値の型名 (type() 組み込み関数と同じ)。
func TypeName(v Value) string { return typeName(v) }

// format は値を文字列にする。quote=true なら文字列を "..." で囲む (配列の要素など)。
func format(v Value, quote bool) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return formatNumber(x)
	case string:
		if quote {
			return strconv.Quote(x)
		}
		return x
	case *Array:
		parts := make([]string, len(x.Elems))
		for i, e := range x.Elems {
			parts[i] = format(e, true)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Map:
		parts := make([]string, len(x.Keys))
		for i, k := range x.Keys {
			parts[i] = k + ": " + format(x.M[k], true)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case *Closure:
		return "<fn " + protoName(x.P) + ">"
	case *Builtin:
		return "<builtin " + x.Name + ">"
	case *Proto:
		return "<proto " + protoName(x) + ">"
	}
	return "?"
}
