package minilang

import "fmt"

type local struct {
	slot    int
	isConst bool
}

type freeVar struct {
	name    string
	isConst bool
	cap     Capture
}

type loopInfo struct {
	continueAt int   // continue のジャンプ先
	breaks     []int // break の JUMP 命令位置 (ループ終了後にパッチ)
}

// funcState はコンパイル中の関数 1 つ分の状態。
type funcState struct {
	proto  *Proto
	parent *funcState
	scopes []map[string]local // ブロックスコープのスタック
	frees  []freeVar
	loops  []*loopInfo
}

// Compiler は AST をバイトコードに変換する。
// トップレベル (どのブロックにも入っていない) の let/const/fn はグローバル変数、
// それ以外はすべてローカル変数 (スロット番号でアクセス) になる。
type Compiler struct {
	fs      *funcState
	globals map[string]bool // 宣言済みグローバル (重複宣言チェック用)
	repl    bool
}

func NewCompiler(repl bool) *Compiler {
	c := &Compiler{globals: map[string]bool{}, repl: repl}
	for _, b := range builtins {
		c.globals[b.Name] = true
	}
	return c
}

func (c *Compiler) Compile(prog []Stmt) (proto *Proto, err error) {
	c.fs = &funcState{proto: &Proto{Name: "<main>"}}
	defer func() {
		if r := recover(); r != nil {
			le, ok := r.(*LangError)
			if !ok {
				panic(r)
			}
			err = le
		}
	}()
	for i, s := range prog {
		// REPL では最後の式文の値を返して表示する
		if es, ok := s.(*ExprStmt); ok && c.repl && i == len(prog)-1 {
			c.expr(es.X)
			c.emit(OpReturn, es.Pos())
			return c.fs.proto, nil
		}
		c.stmt(s)
	}
	end := Pos{}
	if len(prog) > 0 {
		end = prog[len(prog)-1].Pos()
	}
	c.emit(OpNil, end)
	c.emit(OpReturn, end)
	return c.fs.proto, nil
}

func (c *Compiler) fail(pos Pos, format string, args ...any) {
	panic(&LangError{Pos: pos, Kind: "compile error", Msg: fmt.Sprintf(format, args...)})
}

func (c *Compiler) emit(op Op, pos Pos, args ...int) int {
	p := c.fs.proto
	at := len(p.Code)
	p.Code = append(p.Code, byte(op))
	p.Pos = append(p.Pos, pos)
	for _, a := range args {
		if a < 0 || a > 0xFFFF {
			c.fail(pos, "operand too large (function too big)")
		}
		p.Code = append(p.Code, byte(a>>8), byte(a))
		p.Pos = append(p.Pos, pos, pos)
	}
	return at
}

// patch は at の位置にある 2 バイトオペランドを現在のコード位置で書き換える。
func (c *Compiler) patch(at int) {
	code := c.fs.proto.Code
	target := len(code)
	code[at], code[at+1] = byte(target>>8), byte(target)
}

func (c *Compiler) addConst(v Value) int {
	p := c.fs.proto
	if _, isProto := v.(*Proto); !isProto {
		for i, k := range p.Consts {
			if k == v {
				return i
			}
		}
	}
	p.Consts = append(p.Consts, v)
	return len(p.Consts) - 1
}

// ---- 変数の解決 ----

func (c *Compiler) isGlobalScope() bool { return c.fs.parent == nil && len(c.fs.scopes) == 0 }

// declare は現在のスコープに名前を登録する。グローバルなら global=true。
func (c *Compiler) declare(pos Pos, name string, isConst bool) (global bool, slot int) {
	if c.isGlobalScope() {
		if c.globals[name] && !c.repl {
			c.fail(pos, "%s is already declared", name)
		}
		c.globals[name] = true
		return true, 0
	}
	scope := c.fs.scopes[len(c.fs.scopes)-1]
	if _, exists := scope[name]; exists {
		c.fail(pos, "%s is already declared in this scope", name)
	}
	slot = c.fs.proto.NumLocals
	c.fs.proto.NumLocals++
	c.fs.proto.LocalNames = append(c.fs.proto.LocalNames, name)
	scope[name] = local{slot, isConst}
	return false, slot
}

func (c *Compiler) define(pos Pos, name string, isConst bool) {
	global, slot := c.declare(pos, name, isConst)
	if global {
		cst := 0
		if isConst {
			cst = 1
		}
		c.emit(OpDefGlobal, pos, c.addConst(name), cst)
	} else {
		c.emit(OpDefLocal, pos, slot)
	}
}

func lookupLocal(fs *funcState, name string) (local, bool) {
	for i := len(fs.scopes) - 1; i >= 0; i-- {
		if l, ok := fs.scopes[i][name]; ok {
			return l, true
		}
	}
	return local{}, false
}

// resolveFree は外側の関数の変数を探し、見つかれば捕捉リストに加える。
func resolveFree(fs *funcState, name string) (idx int, isConst bool, ok bool) {
	for i, f := range fs.frees {
		if f.name == name {
			return i, f.isConst, true
		}
	}
	if fs.parent == nil {
		return 0, false, false
	}
	var fv freeVar
	if l, found := lookupLocal(fs.parent, name); found {
		fv = freeVar{name, l.isConst, Capture{true, l.slot}}
	} else if i, cst, found := resolveFree(fs.parent, name); found {
		fv = freeVar{name, cst, Capture{false, i}}
	} else {
		return 0, false, false
	}
	fs.frees = append(fs.frees, fv)
	return len(fs.frees) - 1, fv.isConst, true
}

func (c *Compiler) load(pos Pos, name string) {
	if l, ok := lookupLocal(c.fs, name); ok {
		c.emit(OpGetLocal, pos, l.slot)
	} else if i, _, ok := resolveFree(c.fs, name); ok {
		c.emit(OpGetFree, pos, i)
	} else {
		c.emit(OpGetGlobal, pos, c.addConst(name))
	}
}

func (c *Compiler) store(pos Pos, name string) {
	if l, ok := lookupLocal(c.fs, name); ok {
		if l.isConst {
			c.fail(pos, "cannot assign to const %s", name)
		}
		c.emit(OpSetLocal, pos, l.slot)
	} else if i, cst, ok := resolveFree(c.fs, name); ok {
		if cst {
			c.fail(pos, "cannot assign to const %s", name)
		}
		c.emit(OpSetFree, pos, i)
	} else {
		c.emit(OpSetGlobal, pos, c.addConst(name)) // const チェックは実行時
	}
}

func (c *Compiler) pushScope() { c.fs.scopes = append(c.fs.scopes, map[string]local{}) }
func (c *Compiler) popScope()  { c.fs.scopes = c.fs.scopes[:len(c.fs.scopes)-1] }

func (c *Compiler) block(body []Stmt) {
	c.pushScope()
	for _, s := range body {
		c.stmt(s)
	}
	c.popScope()
}

// ---- 文 ----

func (c *Compiler) stmt(s Stmt) {
	pos := s.Pos()
	switch s := s.(type) {
	case *LetStmt:
		if s.IsFn {
			global, slot := c.declare(pos, s.Name, true)
			if global {
				c.function(s.Value.(*FuncLit))
				c.emit(OpDefGlobal, pos, c.addConst(s.Name), 1)
			} else {
				// 先に空のセルを作り、クロージャがそれを捕捉できるようにする (再帰用)
				c.emit(OpNil, pos)
				c.emit(OpDefLocal, pos, slot)
				c.function(s.Value.(*FuncLit))
				c.emit(OpSetLocal, pos, slot)
				c.emit(OpPop, pos)
			}
			return
		}
		c.expr(s.Value)
		c.define(pos, s.Name, s.Const)
	case *ExprStmt:
		c.expr(s.X)
		c.emit(OpPop, pos)
	case *BlockStmt:
		c.block(s.Body)
	case *IfStmt:
		c.expr(s.Cond)
		jElse := c.emit(OpJumpIfFalse, pos, 0)
		c.block(s.Then)
		jEnd := c.emit(OpJump, pos, 0)
		c.patch(jElse + 1)
		if s.Else != nil {
			c.stmt(s.Else)
		}
		c.patch(jEnd + 1)
	case *WhileStmt:
		start := len(c.fs.proto.Code)
		c.expr(s.Cond)
		jExit := c.emit(OpJumpIfFalse, pos, 0)
		c.loop(start, s.Body, nil)
		c.emit(OpJump, pos, start)
		c.patch(jExit + 1)
		c.endLoop()
	case *ForInStmt:
		// 隠しローカル (コレクション, インデックス) を持つループに展開する
		c.pushScope()
		c.expr(s.Iter)
		c.emit(OpIterPrep, pos)
		_, coll := c.declare(pos, " coll", false)
		c.emit(OpDefLocal, pos, coll)
		c.emit(OpConst, pos, c.addConst(0.0))
		_, idx := c.declare(pos, " idx", false)
		c.emit(OpDefLocal, pos, idx)
		start := c.emit(OpForIter, pos, coll, idx, 0)
		c.loop(start, s.Body, func() {
			// ループ変数は反復ごとに新しいセル (クロージャで捕捉しても値がずれない)
			c.define(pos, s.Var, false)
		})
		c.emit(OpJump, pos, start)
		c.patch(start + 5)
		c.endLoop()
		c.popScope()
	case *ReturnStmt:
		if s.Value != nil {
			c.expr(s.Value)
		} else {
			c.emit(OpNil, pos)
		}
		c.emit(OpReturn, pos)
	case *BreakStmt, *ContinueStmt:
		if len(c.fs.loops) == 0 {
			c.fail(pos, "break/continue outside of loop")
		}
		lp := c.fs.loops[len(c.fs.loops)-1]
		if _, ok := s.(*BreakStmt); ok {
			lp.breaks = append(lp.breaks, c.emit(OpJump, pos, 0))
		} else {
			c.emit(OpJump, pos, lp.continueAt)
		}
	default:
		c.fail(pos, "unknown statement %T", s)
	}
}

func (c *Compiler) loop(continueAt int, body []Stmt, prologue func()) {
	c.fs.loops = append(c.fs.loops, &loopInfo{continueAt: continueAt})
	c.pushScope()
	if prologue != nil {
		prologue()
	}
	for _, st := range body {
		c.stmt(st)
	}
	c.popScope()
}

func (c *Compiler) endLoop() {
	lp := c.fs.loops[len(c.fs.loops)-1]
	for _, b := range lp.breaks {
		c.patch(b + 1)
	}
	c.fs.loops = c.fs.loops[:len(c.fs.loops)-1]
}

// ---- 式 ----

func (c *Compiler) expr(e Expr) {
	pos := e.Pos()
	switch e := e.(type) {
	case *NumberLit:
		c.emit(OpConst, pos, c.addConst(e.V))
	case *StringLit:
		c.emit(OpConst, pos, c.addConst(e.V))
	case *BoolLit:
		if e.V {
			c.emit(OpTrue, pos)
		} else {
			c.emit(OpFalse, pos)
		}
	case *NilLit:
		c.emit(OpNil, pos)
	case *Ident:
		c.load(pos, e.Name)
	case *ArrayLit:
		for _, x := range e.Elems {
			c.expr(x)
		}
		c.emit(OpArray, pos, len(e.Elems))
	case *MapLit:
		for i, k := range e.Keys {
			c.emit(OpConst, pos, c.addConst(k))
			c.expr(e.Vals[i])
		}
		c.emit(OpMap, pos, len(e.Keys))
	case *FuncLit:
		c.function(e)
	case *Unary:
		c.expr(e.X)
		if e.Op == "-" {
			c.emit(OpNeg, pos)
		} else {
			c.emit(OpNot, pos)
		}
	case *Binary:
		c.expr(e.L)
		switch e.Op {
		case "&&", "||": // 短絡評価
			op := OpJumpIfFalseOrPop
			if e.Op == "||" {
				op = OpJumpIfTrueOrPop
			}
			j := c.emit(op, pos, 0)
			c.expr(e.R)
			c.patch(j + 1)
		default:
			c.expr(e.R)
			c.emit(binOps[e.Op], pos)
		}
	case *Assign:
		switch t := e.Target.(type) {
		case *Ident:
			c.expr(e.Value)
			c.store(pos, t.Name)
		case *Index:
			c.expr(t.X)
			c.expr(t.Key)
			c.expr(e.Value)
			c.emit(OpSetIndex, pos)
		}
	case *Call:
		c.expr(e.Fn)
		for _, a := range e.Args {
			c.expr(a)
		}
		c.emit(OpCall, pos, len(e.Args))
	case *Index:
		c.expr(e.X)
		c.expr(e.Key)
		c.emit(OpIndex, pos)
	default:
		c.fail(pos, "unknown expression %T", e)
	}
}

// function は関数本体を新しい Proto にコンパイルし、CLOSURE 命令を出す。
func (c *Compiler) function(f *FuncLit) {
	fs := &funcState{
		proto:  &Proto{Name: f.Name, NumParams: len(f.Params)},
		parent: c.fs,
		scopes: []map[string]local{{}},
	}
	c.fs = fs
	for _, p := range f.Params {
		c.declare(f.Pos(), p, false) // 引数はスロット 0..n-1
	}
	for _, s := range f.Body {
		c.stmt(s)
	}
	c.emit(OpNil, f.Pos())
	c.emit(OpReturn, f.Pos())
	for _, fv := range fs.frees {
		fs.proto.Captures = append(fs.proto.Captures, fv.cap)
		fs.proto.FreeNames = append(fs.proto.FreeNames, fv.name)
	}
	c.fs = fs.parent
	c.emit(OpClosure, f.Pos(), c.addConst(fs.proto))
}
