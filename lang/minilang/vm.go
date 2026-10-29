package minilang

import (
	"fmt"
	"io"
	"math"
)

const (
	stackSize = 1 << 18
	maxFrames = 10000
)

type frame struct {
	cl   *Closure
	ip   int // 次に読む命令の位置
	op   int // 実行中の命令の位置 (エラー表示用)
	base int // このフレームのローカル変数 0 番のスタック位置
}

// VM はスタックマシン。
// スタックのレイアウト (関数呼び出し中):
//
//	[... callee | local0(=arg0) local1 ... localN-1 | 一時値 ...]
//	             ^base                                ^sp
type VM struct {
	stack   []Value
	sp      int
	frames  []frame
	globals map[string]Value
	consts  map[string]bool // const なグローバル
	out     io.Writer

	// ステップ実行の状態
	done     bool
	err      error
	result   Value
	steps    int         // 実行した命令数
	ids      map[any]int // スナップショット用: セル・クロージャ・配列などの通し番号
	idNext   map[string]int
	protoIdx map[*Proto]int
}

type rtError string

func NewVM(out io.Writer) *VM {
	vm := &VM{stack: make([]Value, stackSize), globals: map[string]Value{}, consts: map[string]bool{}, out: out}
	for _, b := range builtins {
		vm.globals[b.Name] = b
		vm.consts[b.Name] = true
	}
	return vm
}

func (vm *VM) fail(format string, args ...any) { panic(rtError(fmt.Sprintf(format, args...))) }

func (vm *VM) push(v Value) {
	if vm.sp >= len(vm.stack) {
		vm.fail("stack overflow")
	}
	vm.stack[vm.sp] = v
	vm.sp++
}

func (vm *VM) pop() Value {
	vm.sp--
	return vm.stack[vm.sp]
}

// Run はトップレベルの Proto を実行し、その戻り値を返す。
func (vm *VM) Run(p *Proto) (result Value, err error) {
	vm.Start(p)
	defer vm.recoverError(&err)
	for !vm.done {
		vm.step()
	}
	return vm.result, nil
}

// Start は p を実行する準備をする (ステップ実行用)。
// その後 Step / RunFor / StepOver / StepOut で少しずつ進める。
func (vm *VM) Start(p *Proto) {
	vm.frames = vm.frames[:0]
	for i := 0; i < p.NumLocals; i++ {
		vm.stack[i] = nil
	}
	vm.sp = p.NumLocals
	vm.frames = append(vm.frames, frame{cl: &Closure{P: p}})
	vm.done, vm.err, vm.result = false, nil, nil
	vm.steps = 0
	vm.ids, vm.idNext, vm.protoIdx = nil, nil, nil
}

// recoverError は実行時エラーの panic を LangError (スタックトレース付き) に変換する。
// エラー時のフレームはそのまま残すので、スナップショットで落ちた場所を見られる。
func (vm *VM) recoverError(err *error) {
	r := recover()
	if r == nil {
		return
	}
	msg, ok := r.(rtError)
	if !ok {
		panic(r)
	}
	le := &LangError{Kind: "runtime error", Msg: string(msg)}
	for i := len(vm.frames) - 1; i >= 0; i-- {
		fr := vm.frames[i]
		pos := fr.cl.P.Pos[fr.op]
		if i == len(vm.frames)-1 {
			le.Pos = pos
		}
		if n := len(vm.frames) - 1 - i; n == 10 && i > 5 {
			le.Trace = append(le.Trace, fmt.Sprintf("... (%d frames omitted)", i-5))
			i = 5
		} else if len(vm.frames) > 1 {
			le.Trace = append(le.Trace, fmt.Sprintf("%s (%s)", protoName(fr.cl.P), pos))
		}
	}
	vm.done, vm.err = true, le
	*err = le
}

// step は 1 命令だけ実行する。最後の RETURN で vm.done が立つ。
func (vm *VM) step() {
	{
		f := &vm.frames[len(vm.frames)-1]
		code := f.cl.P.Code
		f.op = f.ip
		op := Op(code[f.ip])
		f.ip++
		vm.steps++
		arg := func() int {
			v := u16(code, f.ip)
			f.ip += 2
			return v
		}
		switch op {
		case OpConst:
			vm.push(f.cl.P.Consts[arg()])
		case OpNil:
			vm.push(nil)
		case OpTrue:
			vm.push(true)
		case OpFalse:
			vm.push(false)
		case OpPop:
			vm.sp--
		case OpGetLocal:
			vm.push(vm.stack[f.base+arg()].(*Cell).V)
		case OpSetLocal:
			vm.stack[f.base+arg()].(*Cell).V = vm.stack[vm.sp-1]
		case OpDefLocal:
			vm.stack[f.base+arg()] = &Cell{V: vm.pop()}
		case OpGetFree:
			vm.push(f.cl.Free[arg()].V)
		case OpSetFree:
			f.cl.Free[arg()].V = vm.stack[vm.sp-1]
		case OpGetGlobal:
			name := f.cl.P.Consts[arg()].(string)
			v, ok := vm.globals[name]
			if !ok {
				vm.fail("undefined variable %s", name)
			}
			vm.push(v)
		case OpSetGlobal:
			name := f.cl.P.Consts[arg()].(string)
			if _, ok := vm.globals[name]; !ok {
				vm.fail("undefined variable %s (declare it with let first)", name)
			}
			if vm.consts[name] {
				vm.fail("cannot assign to const %s", name)
			}
			vm.globals[name] = vm.stack[vm.sp-1]
		case OpDefGlobal:
			name := f.cl.P.Consts[arg()].(string)
			vm.consts[name] = arg() == 1
			vm.globals[name] = vm.pop()
		case OpAdd, OpSub, OpMul, OpDiv, OpMod, OpEq, OpNe, OpLt, OpLe, OpGt, OpGe:
			// エラー時にオペランドがスタックに残って見えるよう、成功してから pop する
			r := vm.binop(op, vm.stack[vm.sp-2], vm.stack[vm.sp-1])
			vm.sp -= 2
			vm.push(r)
		case OpNeg:
			n, ok := vm.stack[vm.sp-1].(float64)
			if !ok {
				vm.fail("cannot apply - to %s", typeName(vm.stack[vm.sp-1]))
			}
			vm.stack[vm.sp-1] = -n
		case OpNot:
			vm.stack[vm.sp-1] = !truthy(vm.stack[vm.sp-1])
		case OpJump:
			f.ip = arg()
		case OpJumpIfFalse:
			t := arg()
			if !truthy(vm.pop()) {
				f.ip = t
			}
		case OpJumpIfFalseOrPop, OpJumpIfTrueOrPop:
			t := arg()
			if truthy(vm.stack[vm.sp-1]) == (op == OpJumpIfTrueOrPop) {
				f.ip = t
			} else {
				vm.sp--
			}
		case OpCall:
			vm.call(arg())
		case OpReturn:
			res := vm.pop()
			fr := vm.frames[len(vm.frames)-1]
			vm.frames = vm.frames[:len(vm.frames)-1]
			if len(vm.frames) == 0 {
				vm.frames = append(vm.frames, fr) // 終了後も最後の位置を見られるように残す
				vm.done, vm.result = true, res
				return
			}
			vm.sp = fr.base - 1 // 引数・ローカルと callee 自身を捨てる
			vm.push(res)
		case OpClosure:
			p := f.cl.P.Consts[arg()].(*Proto)
			cl := &Closure{P: p, Free: make([]*Cell, len(p.Captures))}
			for i, c := range p.Captures {
				if c.FromLocal {
					cl.Free[i] = vm.stack[f.base+c.Index].(*Cell)
				} else {
					cl.Free[i] = f.cl.Free[c.Index]
				}
			}
			vm.push(cl)
		case OpArray:
			n := arg()
			elems := make([]Value, n)
			copy(elems, vm.stack[vm.sp-n:vm.sp])
			vm.sp -= n
			vm.push(&Array{elems})
		case OpMap:
			n := arg()
			m := NewMap()
			for i := vm.sp - 2*n; i < vm.sp; i += 2 {
				m.Set(vm.stack[i].(string), vm.stack[i+1])
			}
			vm.sp -= 2 * n
			vm.push(m)
		case OpIndex:
			key := vm.pop()
			obj := vm.pop()
			vm.push(vm.index(obj, key))
		case OpSetIndex:
			val := vm.pop()
			key := vm.pop()
			obj := vm.pop()
			vm.setIndex(obj, key, val)
			vm.push(val)
		case OpIterPrep:
			switch c := vm.pop().(type) {
			case *Array:
				vm.push(c)
			case *Map:
				ks := make([]Value, len(c.Keys))
				for i, k := range c.Keys {
					ks[i] = k
				}
				vm.push(&Array{ks})
			default:
				vm.fail("cannot iterate over %s", typeName(c))
			}
		case OpForIter:
			coll := vm.stack[f.base+arg()].(*Cell).V.(*Array)
			idx := vm.stack[f.base+arg()].(*Cell)
			exit := arg()
			i := int(idx.V.(float64))
			if i >= len(coll.Elems) {
				f.ip = exit
			} else {
				idx.V = float64(i + 1)
				vm.push(coll.Elems[i])
			}
		default:
			vm.fail("unknown opcode %d", op)
		}
	}
}

func (vm *VM) call(n int) {
	switch fn := vm.stack[vm.sp-n-1].(type) {
	case *Closure:
		p := fn.P
		if n != p.NumParams {
			vm.fail("%s expects %d argument(s) but got %d", protoName(p), p.NumParams, n)
		}
		base := vm.sp - n
		if len(vm.frames) >= maxFrames || base+p.NumLocals >= len(vm.stack) {
			vm.fail("stack overflow (too deep recursion)")
		}
		for i := 0; i < n; i++ { // 引数をセルに包んでローカル変数にする
			vm.stack[base+i] = &Cell{V: vm.stack[base+i]}
		}
		for i := n; i < p.NumLocals; i++ {
			vm.stack[base+i] = nil
		}
		vm.sp = base + p.NumLocals
		vm.frames = append(vm.frames, frame{cl: fn, base: base})
	case *Builtin:
		args := append([]Value(nil), vm.stack[vm.sp-n:vm.sp]...)
		res, err := fn.Fn(vm, args)
		if err != nil {
			vm.fail("%s: %s", fn.Name, err)
		}
		vm.sp -= n + 1
		vm.push(res)
	default:
		vm.fail("cannot call %s", typeName(fn))
	}
}

func (vm *VM) binop(op Op, a, b Value) Value {
	switch op {
	case OpEq: // 型が違えば常に false。配列・マップ・関数は同一性で比較
		return a == b
	case OpNe:
		return a != b
	}
	if x, ok := a.(float64); ok {
		if y, ok := b.(float64); ok {
			switch op {
			case OpAdd:
				return x + y
			case OpSub:
				return x - y
			case OpMul:
				return x * y
			case OpDiv, OpMod:
				if y == 0 {
					vm.fail("division by zero")
				}
				if op == OpDiv {
					return x / y
				}
				return math.Mod(x, y)
			case OpLt:
				return x < y
			case OpLe:
				return x <= y
			case OpGt:
				return x > y
			case OpGe:
				return x >= y
			}
		}
	}
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			switch op {
			case OpAdd:
				return x + y
			case OpLt:
				return x < y
			case OpLe:
				return x <= y
			case OpGt:
				return x > y
			case OpGe:
				return x >= y
			}
		}
	}
	vm.fail("cannot apply %s to %s and %s", opSymbol[op], typeName(a), typeName(b))
	return nil
}

func (vm *VM) toIndex(key Value, length int) int {
	n, ok := key.(float64)
	if !ok {
		vm.fail("index must be a number, got %s", typeName(key))
	}
	if n != math.Trunc(n) {
		vm.fail("index must be an integer, got %s", formatNumber(n))
	}
	if n < 0 || int(n) >= length {
		vm.fail("index %s out of range (length %d)", formatNumber(n), length)
	}
	return int(n)
}

func (vm *VM) mapKey(key Value) string {
	k, ok := key.(string)
	if !ok {
		vm.fail("map key must be a string, got %s", typeName(key))
	}
	return k
}

func (vm *VM) index(obj, key Value) Value {
	switch o := obj.(type) {
	case *Array:
		return o.Elems[vm.toIndex(key, len(o.Elems))]
	case string:
		rs := []rune(o)
		return string(rs[vm.toIndex(key, len(rs))])
	case *Map:
		k := vm.mapKey(key)
		v, ok := o.M[k]
		if !ok {
			vm.fail("key %q not found in map (use has() to check)", k)
		}
		return v
	}
	vm.fail("cannot index %s", typeName(obj))
	return nil
}

func (vm *VM) setIndex(obj, key, val Value) {
	switch o := obj.(type) {
	case *Array:
		o.Elems[vm.toIndex(key, len(o.Elems))] = val
	case *Map:
		o.Set(vm.mapKey(key), val)
	default:
		vm.fail("cannot assign into %s", typeName(obj))
	}
}
