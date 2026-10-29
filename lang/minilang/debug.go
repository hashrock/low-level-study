package minilang

import (
	"io"
	"strings"
)

// ---- ステップ実行 API ----
//
//	vm := NewVM(&buf)   // print の出力先 (bytes.Buffer など)
//	vm.Start(proto)
//	for !vm.Done() { vm.Step() }  // 1 命令ずつ
//
// どの関数も実行時エラーは error (*LangError) として返し、以後 Done() は true になる。

// Done は実行が終わった (正常終了またはエラー) かどうか。
func (vm *VM) Done() bool { return vm.done }

// Err は実行時エラー (なければ nil)。
func (vm *VM) Err() error { return vm.err }

// Result はトップレベルの戻り値。
func (vm *VM) Result() Value { return vm.result }

// Steps は Start からの実行命令数。
func (vm *VM) Steps() int { return vm.steps }

// SetOutput は print の出力先を差し替える。
func (vm *VM) SetOutput(w io.Writer) { vm.out = w }

// Depth は呼び出しフレームの深さ (トップレベルは 1)。
func (vm *VM) Depth() int { return len(vm.frames) }

// Step は 1 命令だけ実行する。
func (vm *VM) Step() error {
	_, err := vm.runUntil(1, func() bool { return true })
	return err
}

// RunFor は最大 n 命令実行する (命令数の予算)。終了すると早めに戻る。
// 無限ループでもブラウザを固めないよう、呼び出し側は小分けに呼ぶ。
func (vm *VM) RunFor(n int) error {
	_, err := vm.runUntil(n, func() bool { return false })
	return err
}

// StepOver は現在の行の実行が終わるまで進める (呼び出した関数の中では止まらない)。
// 次のどれかで止まる: 同じフレームで別の行に移った / 後ろ向きにジャンプした (ループの次の周回) /
// 関数から戻った / 終了した。budget 命令使い切ったら stopped=false で戻る。
func (vm *VM) StepOver(budget int) (stopped bool, err error) {
	if vm.done {
		return true, vm.err
	}
	d0 := len(vm.frames)
	line0 := vm.curPos().Line
	lastIP := vm.frames[d0-1].ip
	return vm.runUntil(budget, func() bool {
		d := len(vm.frames)
		if d < d0 {
			return true
		}
		if d > d0 {
			return false
		}
		ip := vm.frames[d-1].ip
		back := ip < lastIP // 後ろへジャンプした = ループの次の周回
		lastIP = ip
		return back || vm.curPos().Line != line0
	})
}

// StepOut は現在の関数から戻るまで進める (トップレベルなら最後まで)。
func (vm *VM) StepOut(budget int) (stopped bool, err error) {
	if vm.done {
		return true, vm.err
	}
	d0 := len(vm.frames)
	return vm.runUntil(budget, func() bool { return len(vm.frames) < d0 })
}

// runUntil は最大 budget 命令、stop() が true になるか終了するまで実行する。
func (vm *VM) runUntil(budget int, stop func() bool) (stopped bool, err error) {
	if vm.done {
		return true, vm.err
	}
	defer vm.recoverError(&err)
	for i := 0; i < budget; i++ {
		vm.step()
		if vm.done || stop() {
			return true, nil
		}
	}
	return false, nil
}

// curPos は次に実行する命令のソース位置。
func (vm *VM) curPos() Pos {
	f := &vm.frames[len(vm.frames)-1]
	ip := f.ip
	if vm.done || ip >= len(f.cl.P.Pos) {
		ip = f.op
	}
	return f.cl.P.Pos[ip]
}

// ---- スナップショット ----

// SValue は表示用の値。
type SValue struct {
	Type string `json:"t"`            // nil bool number string array map function cell empty
	Text string `json:"s"`            // 表示文字列 (長いものは省略)
	ID   int    `json:"id,omitempty"` // セル・クロージャ・配列・マップの通し番号
}

type SInstr struct {
	Proto int    `json:"proto"` // Protos() の添字
	Func  string `json:"func"`
	Line  int    `json:"line"`
	Col   int    `json:"col"`
	Instr
}

type SLocal struct {
	Slot   int    `json:"slot"`
	Name   string `json:"name"`
	Hidden bool   `json:"hidden,omitempty"` // for-in の隠しローカル
	Cell   int    `json:"cell"`             // 0 = まだ定義されていない
	Value  SValue `json:"v"`
}

type SFrame struct {
	Func   string   `json:"func"`
	Proto  int      `json:"proto"`
	IP     int      `json:"ip"`
	Line   int      `json:"line"`
	Col    int      `json:"col"`
	Base   int      `json:"base"`
	Locals []SLocal `json:"locals"`
	Free   []SLocal `json:"free"` // クロージャが捕捉したセル
}

type SSlot struct {
	Index int    `json:"i"`
	Frame int    `json:"frame"` // どのフレームの領域か (-1 = なし)
	Role  string `json:"role"`  // callee / local / temp
	Name  string `json:"name,omitempty"`
	Value SValue `json:"v"`
}

type SGlobal struct {
	Name    string `json:"name"`
	Const   bool   `json:"const,omitempty"`
	Builtin bool   `json:"builtin,omitempty"`
	Value   SValue `json:"v"`
}

type SClosure struct {
	ID    int      `json:"id"`
	Func  string   `json:"func"`
	Proto int      `json:"proto"`
	Free  []SLocal `json:"free"`
}

type SCell struct {
	ID    int      `json:"id"`
	Value SValue   `json:"v"`
	Refs  []string `json:"refs"` // このセルを指しているもの (関数のローカル / クロージャ)
}

type SError struct {
	Line  int      `json:"line"`
	Col   int      `json:"col"`
	Kind  string   `json:"kind"`
	Msg   string   `json:"msg"`
	Trace []string `json:"trace,omitempty"`
	Text  string   `json:"text"`
}

// Snapshot は VM の内部状態をまるごと表示用に変換したもの。
type Snapshot struct {
	Done         bool       `json:"done"`
	Steps        int        `json:"steps"`
	Result       *SValue    `json:"result,omitempty"`
	Error        *SError    `json:"error,omitempty"`
	Cur          SInstr     `json:"cur"` // 次に実行する命令 (終了後は最後に実行した命令)
	SP           int        `json:"sp"`
	Stack        []SSlot    `json:"stack"` // 下から上へ (上限あり)
	StackOmitted int        `json:"stackOmitted"`
	Frames       []SFrame   `json:"frames"` // 外側から内側へ (上限あり)
	FrameOmitted int        `json:"frameOmitted"`
	Globals      []SGlobal  `json:"globals"`
	Closures     []SClosure `json:"closures"`
	Cells        []SCell    `json:"cells"` // クロージャが捕捉しているセル
}

const (
	snapMaxStack  = 200
	snapMaxFrames = 50
	snapMaxText   = 80
	snapMaxObjs   = 300
)

// ErrorInfo は error を表示用に変換する (LangError 以外は位置 0:0)。
func ErrorInfo(err error) *SError {
	if err == nil {
		return nil
	}
	if le, ok := err.(*LangError); ok {
		return &SError{Line: le.Pos.Line, Col: le.Pos.Col, Kind: le.Kind, Msg: le.Msg, Trace: le.Trace, Text: le.Error()}
	}
	return &SError{Kind: "error", Msg: err.Error(), Text: err.Error()}
}

// Protos は root とその中の関数をすべて (Disassemble と同じ順に) 列挙する。
func Protos(root *Proto) []*Proto {
	var out []*Proto
	var walk func(p *Proto)
	walk = func(p *Proto) {
		out = append(out, p)
		for _, c := range p.Consts {
			if sub, ok := c.(*Proto); ok {
				walk(sub)
			}
		}
	}
	walk(root)
	return out
}

func (vm *VM) protoIndex(p *Proto) int {
	if vm.protoIdx == nil {
		vm.protoIdx = map[*Proto]int{}
		for i, q := range Protos(vm.frames[0].cl.P) {
			vm.protoIdx[q] = i
		}
	}
	if i, ok := vm.protoIdx[p]; ok {
		return i
	}
	return -1
}

// id はオブジェクトに種類ごとの通し番号を振る (スナップショットをまたいで同じ番号)。
func (vm *VM) id(kind string, p any) int {
	if vm.ids == nil {
		vm.ids = map[any]int{}
		vm.idNext = map[string]int{}
	}
	if n, ok := vm.ids[p]; ok {
		return n
	}
	vm.idNext[kind]++
	vm.ids[p] = vm.idNext[kind]
	return vm.ids[p]
}

// sval は値を表示用に変換する。循環しているデータでも止まるよう深さと長さを制限する。
func (vm *VM) sval(v Value) SValue {
	sv := SValue{Type: typeName(v), Text: clip(vm.show(v, 0))}
	switch x := v.(type) {
	case *Closure:
		sv.ID = vm.id("fn", x)
	case *Array:
		sv.ID = vm.id("array", x)
	case *Map:
		sv.ID = vm.id("map", x)
	case *Builtin:
		sv.Type = "builtin"
	case *Cell:
		sv = vm.sval(x.V)
		sv.Type, sv.ID = "cell:"+sv.Type, vm.id("cell", x)
	}
	return sv
}

func clip(s string) string {
	if r := []rune(s); len(r) > snapMaxText {
		return string(r[:snapMaxText-1]) + "…"
	}
	return s
}

func (vm *VM) show(v Value, depth int) string {
	switch x := v.(type) {
	case *Array:
		if depth > 2 {
			return "[…]"
		}
		var b strings.Builder
		b.WriteString("[")
		for i, e := range x.Elems {
			if i > 0 {
				b.WriteString(", ")
			}
			if b.Len() > snapMaxText {
				b.WriteString("…")
				break
			}
			b.WriteString(vm.show(e, depth+1))
		}
		return b.String() + "]"
	case *Map:
		if depth > 2 {
			return "{…}"
		}
		var b strings.Builder
		b.WriteString("{")
		for i, k := range x.Keys {
			if i > 0 {
				b.WriteString(", ")
			}
			if b.Len() > snapMaxText {
				b.WriteString("…")
				break
			}
			b.WriteString(k + ": " + vm.show(x.M[k], depth+1))
		}
		return b.String() + "}"
	case *Closure:
		return "<fn " + protoName(x.P) + " #" + itoa(vm.id("fn", x)) + ">"
	case *Cell:
		return vm.show(x.V, depth)
	}
	return format(v, true)
}

func itoa(n int) string { return formatNumber(float64(n)) }

func (vm *VM) slot(cell Value, name string, slot int) SLocal {
	l := SLocal{Slot: slot, Name: strings.TrimSpace(name), Hidden: strings.HasPrefix(name, " ")}
	if c, ok := cell.(*Cell); ok {
		l.Cell = vm.id("cell", c)
		l.Value = vm.sval(c.V)
	} else {
		l.Value = SValue{Type: "empty", Text: "(未定義)"}
	}
	return l
}

// Snapshot は現在の VM の状態を返す。Start 前に呼んではいけない。
func (vm *VM) Snapshot() *Snapshot {
	s := &Snapshot{Done: vm.done, Steps: vm.steps, SP: vm.sp, Error: ErrorInfo(vm.err)}
	if vm.done && vm.err == nil {
		r := vm.sval(vm.result)
		s.Result = &r
	}

	// 現在の命令
	top := &vm.frames[len(vm.frames)-1]
	ip := top.ip
	if vm.done || ip >= len(top.cl.P.Code) {
		ip = top.op
	}
	pos := top.cl.P.Pos[ip]
	s.Cur = SInstr{Proto: vm.protoIndex(top.cl.P), Func: protoName(top.cl.P), Line: pos.Line, Col: pos.Col, Instr: DecodeInstr(top.cl.P, ip)}

	// フレームとスタックの各スロットの役割
	role := make([]SSlot, vm.sp)
	for i := range role {
		role[i] = SSlot{Index: i, Frame: -1, Role: "temp"}
	}
	first := 0
	if len(vm.frames) > snapMaxFrames {
		first = len(vm.frames) - snapMaxFrames
		s.FrameOmitted = first
	}
	for fi, f := range vm.frames {
		p := f.cl.P
		for i := 0; i < p.NumLocals && f.base+i < vm.sp; i++ {
			role[f.base+i] = SSlot{Index: f.base + i, Frame: fi, Role: "local", Name: strings.TrimSpace(nameAt(p.LocalNames, i))}
		}
		if fi > 0 && f.base-1 >= 0 && f.base-1 < vm.sp {
			role[f.base-1] = SSlot{Index: f.base - 1, Frame: fi, Role: "callee", Name: protoName(p)}
		}
		end := vm.sp // このフレームの一時値は次のフレームの callee の手前まで
		if fi+1 < len(vm.frames) {
			end = vm.frames[fi+1].base - 1
		}
		for i := f.base + p.NumLocals; i < end && i < vm.sp; i++ {
			role[i].Frame = fi
		}
		if fi < first {
			continue
		}
		fip := f.ip
		if fi == len(vm.frames)-1 {
			fip = ip
		} else {
			fip = f.op // 呼び出し元は CALL 命令の位置を見せる
		}
		fpos := p.Pos[fip]
		sf := SFrame{Func: protoName(p), Proto: vm.protoIndex(p), IP: fip, Line: fpos.Line, Col: fpos.Col, Base: f.base, Locals: []SLocal{}, Free: []SLocal{}}
		for i := 0; i < p.NumLocals; i++ {
			var v Value
			if f.base+i < vm.sp {
				v = vm.stack[f.base+i]
			}
			sf.Locals = append(sf.Locals, vm.slot(v, nameAt(p.LocalNames, i), i))
		}
		for i, c := range f.cl.Free {
			sf.Free = append(sf.Free, vm.slot(c, nameAt(p.FreeNames, i), i))
		}
		s.Frames = append(s.Frames, sf)
	}
	s.Stack = []SSlot{}
	lo := 0
	if vm.sp > snapMaxStack {
		lo = vm.sp - snapMaxStack
		s.StackOmitted = lo
	}
	for i := lo; i < vm.sp; i++ {
		r := role[i]
		r.Value = vm.sval(vm.stack[i])
		s.Stack = append(s.Stack, r)
	}

	// グローバル (組み込み関数は最後に)
	// sval が初見のオブジェクトに #id を振るので、map の反復順ではなく整列後の順で評価する
	s.Globals = []SGlobal{}
	for name, v := range vm.globals {
		_, isB := v.(*Builtin)
		s.Globals = append(s.Globals, SGlobal{Name: name, Const: vm.consts[name], Builtin: isB})
	}
	sortGlobals(s.Globals)
	for i := range s.Globals {
		s.Globals[i].Value = vm.sval(vm.globals[s.Globals[i].Name])
	}

	// 到達できるクロージャと、それが捕捉しているセル
	vm.collectClosures(s)
	return s
}

func sortGlobals(g []SGlobal) {
	less := func(a, b SGlobal) bool {
		if a.Builtin != b.Builtin {
			return !a.Builtin
		}
		return a.Name < b.Name
	}
	for i := 1; i < len(g); i++ { // 挿入ソート (要素数は少ない)
		for j := i; j > 0 && less(g[j], g[j-1]); j-- {
			g[j], g[j-1] = g[j-1], g[j]
		}
	}
}

func (vm *VM) collectClosures(s *Snapshot) {
	seen := map[any]bool{}
	var closures []*Closure
	var visit func(v Value)
	visit = func(v Value) {
		if len(seen) > snapMaxObjs {
			return
		}
		switch x := v.(type) {
		case *Cell:
			if !seen[x] {
				seen[x] = true
				visit(x.V)
			}
		case *Closure:
			if !seen[x] {
				seen[x] = true
				closures = append(closures, x)
				for _, c := range x.Free {
					visit(c)
				}
			}
		case *Array:
			if !seen[x] {
				seen[x] = true
				for _, e := range x.Elems {
					visit(e)
				}
			}
		case *Map:
			if !seen[x] {
				seen[x] = true
				for _, k := range x.Keys {
					visit(x.M[k])
				}
			}
		}
	}
	for i := 0; i < vm.sp; i++ {
		visit(vm.stack[i])
	}
	for _, f := range vm.frames[1:] { // frames[0] はトップレベルの擬似クロージャ
		visit(f.cl)
	}
	names := make([]string, 0, len(vm.globals))
	for _, g := range s.Globals {
		names = append(names, g.Name)
	}
	for _, n := range names {
		visit(vm.globals[n])
	}
	if vm.done && vm.err == nil {
		visit(vm.result)
	}

	// 番号順に並べる
	for i := 1; i < len(closures); i++ {
		for j := i; j > 0 && vm.id("fn", closures[j]) < vm.id("fn", closures[j-1]); j-- {
			closures[j], closures[j-1] = closures[j-1], closures[j]
		}
	}
	cellIdx := map[*Cell]int{}
	s.Closures = []SClosure{}
	s.Cells = []SCell{}
	addRef := func(c *Cell, ref string) {
		i, ok := cellIdx[c]
		if !ok {
			i = len(s.Cells)
			cellIdx[c] = i
			s.Cells = append(s.Cells, SCell{ID: vm.id("cell", c), Value: vm.sval(c.V), Refs: []string{}})
		}
		s.Cells[i].Refs = append(s.Cells[i].Refs, ref)
	}
	for _, cl := range closures {
		sc := SClosure{ID: vm.id("fn", cl), Func: protoName(cl.P), Proto: vm.protoIndex(cl.P), Free: []SLocal{}}
		for i, c := range cl.Free {
			sc.Free = append(sc.Free, vm.slot(c, nameAt(cl.P.FreeNames, i), i))
			addRef(c, sc.Func+" #"+itoa(sc.ID)+" の "+nameAt(cl.P.FreeNames, i))
		}
		s.Closures = append(s.Closures, sc)
	}
	// 捕捉されているセルが、どのフレームのローカルでもあるか
	for fi, f := range vm.frames {
		p := f.cl.P
		for i := 0; i < p.NumLocals && f.base+i < vm.sp; i++ {
			if c, ok := vm.stack[f.base+i].(*Cell); ok {
				if _, captured := cellIdx[c]; captured {
					addRef(c, "フレーム"+itoa(fi)+" "+protoName(p)+" のローカル "+nameAt(p.LocalNames, i))
				}
			}
		}
	}
	for i := 1; i < len(s.Cells); i++ {
		for j := i; j > 0 && s.Cells[j].ID < s.Cells[j-1].ID; j-- {
			s.Cells[j], s.Cells[j-1] = s.Cells[j-1], s.Cells[j]
		}
	}
}
