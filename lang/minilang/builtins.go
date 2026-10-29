package minilang

import (
	"errors"
	"fmt"
	"strings"
)

var builtins []*Builtin

func init() {
	builtins = []*Builtin{
		{"print", func(vm *VM, args []Value) (Value, error) {
			parts := make([]string, len(args))
			for i, a := range args {
				parts[i] = format(a, false)
			}
			fmt.Fprintln(vm.out, strings.Join(parts, " "))
			return nil, nil
		}},
		{"len", func(vm *VM, args []Value) (Value, error) {
			if err := arity(args, 1); err != nil {
				return nil, err
			}
			switch x := args[0].(type) {
			case string:
				return float64(len([]rune(x))), nil
			case *Array:
				return float64(len(x.Elems)), nil
			case *Map:
				return float64(len(x.Keys)), nil
			}
			return nil, fmt.Errorf("cannot take length of %s", typeName(args[0]))
		}},
		{"push", func(vm *VM, args []Value) (Value, error) {
			if err := arity(args, 2); err != nil {
				return nil, err
			}
			a, ok := args[0].(*Array)
			if !ok {
				return nil, fmt.Errorf("first argument must be an array, got %s", typeName(args[0]))
			}
			a.Elems = append(a.Elems, args[1])
			return nil, nil
		}},
		{"pop", func(vm *VM, args []Value) (Value, error) {
			if err := arity(args, 1); err != nil {
				return nil, err
			}
			a, ok := args[0].(*Array)
			if !ok || len(a.Elems) == 0 {
				return nil, errors.New("argument must be a non-empty array")
			}
			v := a.Elems[len(a.Elems)-1]
			a.Elems = a.Elems[:len(a.Elems)-1]
			return v, nil
		}},
		{"keys", func(vm *VM, args []Value) (Value, error) {
			if err := arity(args, 1); err != nil {
				return nil, err
			}
			m, ok := args[0].(*Map)
			if !ok {
				return nil, fmt.Errorf("argument must be a map, got %s", typeName(args[0]))
			}
			ks := make([]Value, len(m.Keys))
			for i, k := range m.Keys {
				ks[i] = k
			}
			return &Array{ks}, nil
		}},
		{"has", func(vm *VM, args []Value) (Value, error) {
			if err := arity(args, 2); err != nil {
				return nil, err
			}
			m, ok := args[0].(*Map)
			k, ok2 := args[1].(string)
			if !ok || !ok2 {
				return nil, errors.New("usage: has(map, string)")
			}
			_, found := m.M[k]
			return found, nil
		}},
		{"type", func(vm *VM, args []Value) (Value, error) {
			if err := arity(args, 1); err != nil {
				return nil, err
			}
			return typeName(args[0]), nil
		}},
		{"str", func(vm *VM, args []Value) (Value, error) {
			if err := arity(args, 1); err != nil {
				return nil, err
			}
			return format(args[0], false), nil
		}},
		{"range", func(vm *VM, args []Value) (Value, error) {
			// range(n) = [0..n), range(a, b) = [a..b)
			from, to := 0.0, 0.0
			var ok1, ok2 bool
			switch len(args) {
			case 1:
				to, ok1 = args[0].(float64)
				ok2 = true
			case 2:
				from, ok1 = args[0].(float64)
				to, ok2 = args[1].(float64)
			}
			if !ok1 || !ok2 {
				return nil, errors.New("usage: range(n) or range(from, to) with numbers")
			}
			arr := &Array{}
			for i := from; i < to; i++ {
				arr.Elems = append(arr.Elems, i)
			}
			return arr, nil
		}},
	}
}

func arity(args []Value, n int) error {
	if len(args) != n {
		return fmt.Errorf("expects %d argument(s) but got %d", n, len(args))
	}
	return nil
}
