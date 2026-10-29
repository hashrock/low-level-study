package minicc

import "fmt"

// 名前解決 + 捕捉解析 (コード生成の前に AST を 1 回なめる)。
//
// 各変数がどこに住むかを決める:
//   - グローバル (トップレベルの宣言)          → .data のラベル g_名前
//   - ローカル (関数内 / ブロック内)          → スタックフレームのスロット
//   - 内側の関数から参照されるローカル (捕捉) → スロットには「箱 (box)」へのポインタを置く
//
// 捕捉された変数だけをヒープの箱に入れるので、普通のローカル変数は
// メモリ 1 回のロード/ストアでアクセスできる。

// Var は宣言 1 つ (let/const/fn/引数/for のループ変数)。
type Var struct {
	Name     string
	Const    bool
	Global   bool
	Builtin  bool
	Captured bool      // 内側の関数から参照される → 箱に入れる
	Fn       *FuncInfo // 宣言した関数 (グローバルなら nil)
	Slot     int       // フレーム内のスロット番号
}

// FuncInfo は関数 1 個分の情報。トップレベルのコードも "mini_main" という関数として扱う。
type FuncInfo struct {
	Lit      *FuncLit // nil ならトップレベル
	Name     string
	Label    string
	Parent   *FuncInfo
	Params   []*Var
	Free     []*Var // 外側から捕捉する変数 (クロージャ object に箱ポインタとして並ぶ)
	NumSlots int
}

func (f *FuncInfo) newSlot() int { f.NumSlots++; return f.NumSlots - 1 }

func (f *FuncInfo) freeIndex(v *Var) int {
	for i, w := range f.Free {
		if w == v {
			return i
		}
	}
	return -1
}

// 組み込み関数と引数の個数 (-1 は可変長)
var builtinArity = map[string]int{
	"print": -1, "len": 1, "push": 2, "pop": 1, "keys": 1, "has": 2,
	"type": 1, "str": 1, "range": -1,
}

type scope struct {
	vars   map[string]*Var
	parent *scope
}

type Resolver struct {
	decl     map[Node]*Var          // *LetStmt / *ForInStmt → 宣言された変数
	uses     map[Node]*Var          // *Ident → 参照先
	funcs    map[*FuncLit]*FuncInfo // 関数リテラル → 情報
	forSlots map[*ForInStmt][2]int  // for-in の隠しスロット (コレクション, 添字)
	all      []*FuncInfo
	topNames map[string]*Var
	builtins map[string]*Var
	main     *FuncInfo
	fn       *FuncInfo
	sc       *scope
	global   *scope
}

func compileErr(p Pos, format string, args ...any) {
	panic(&LangError{Pos: p, Kind: "compile error", Msg: fmt.Sprintf(format, args...)})
}

func Resolve(prog []Stmt) (r *Resolver, err error) {
	defer func() {
		if e := recover(); e != nil {
			le, ok := e.(*LangError)
			if !ok {
				panic(e)
			}
			err = le
		}
	}()
	r = &Resolver{
		decl: map[Node]*Var{}, uses: map[Node]*Var{}, funcs: map[*FuncLit]*FuncInfo{},
		forSlots: map[*ForInStmt][2]int{}, topNames: map[string]*Var{}, builtins: map[string]*Var{},
	}
	r.main = &FuncInfo{Name: "<main>", Label: "mini_main"}
	r.all = []*FuncInfo{r.main}
	r.fn = r.main
	r.global = &scope{vars: map[string]*Var{}}
	r.sc = r.global
	// 先に全トップレベル名を集める: 関数本体からは後で宣言されるグローバルも見える (相互再帰用)
	for _, s := range prog {
		if l, ok := s.(*LetStmt); ok && r.topNames[l.Name] == nil {
			r.topNames[l.Name] = &Var{Name: l.Name, Const: l.Const, Global: true}
		}
	}
	r.stmts(prog)
	return r, nil
}

func (r *Resolver) lookup(name string, p Pos) *Var {
	for s := r.sc; s != nil; s = s.parent {
		if v := s.vars[name]; v != nil {
			return v
		}
	}
	if r.fn != r.main && r.topNames[name] != nil {
		return r.topNames[name]
	}
	if _, ok := builtinArity[name]; ok {
		if r.builtins[name] == nil {
			r.builtins[name] = &Var{Name: name, Builtin: true, Global: true}
		}
		return r.builtins[name]
	}
	compileErr(p, "undefined variable %s", name)
	return nil
}

// use は識別子の参照を記録し、外側の関数のローカルなら捕捉として登録する。
func (r *Resolver) use(id *Ident) *Var {
	v := r.lookup(id.Name, id.P)
	r.uses[id] = v
	if !v.Global && v.Fn != r.fn {
		v.Captured = true
		// 間の関数すべてが箱を受け渡す必要がある (孫関数からの参照)
		for f := r.fn; f != v.Fn; f = f.Parent {
			if f.freeIndex(v) < 0 {
				f.Free = append(f.Free, v)
			}
		}
	}
	return v
}

func (r *Resolver) declare(name string, isConst bool) *Var {
	var v *Var
	if r.sc == r.global {
		v = r.topNames[name]
	} else {
		v = &Var{Name: name, Const: isConst, Fn: r.fn, Slot: r.fn.newSlot()}
	}
	r.sc.vars[name] = v
	return v
}

func (r *Resolver) block(body []Stmt) {
	saved := r.sc
	r.sc = &scope{vars: map[string]*Var{}, parent: saved}
	r.stmts(body)
	r.sc = saved
}

func (r *Resolver) stmts(list []Stmt) {
	for _, s := range list {
		r.stmt(s)
	}
}

func (r *Resolver) stmt(s Stmt) {
	switch s := s.(type) {
	case *LetStmt:
		if s.IsFn { // 再帰できるよう名前を先に束縛
			r.decl[s] = r.declare(s.Name, s.Const)
			r.expr(s.Value)
		} else {
			r.expr(s.Value)
			r.decl[s] = r.declare(s.Name, s.Const)
		}
	case *ExprStmt:
		r.expr(s.X)
	case *BlockStmt:
		r.block(s.Body)
	case *IfStmt:
		r.expr(s.Cond)
		r.block(s.Then)
		if s.Else != nil {
			r.stmt(s.Else)
		}
	case *WhileStmt:
		r.expr(s.Cond)
		r.block(s.Body)
	case *ForInStmt:
		r.expr(s.Iter)
		r.forSlots[s] = [2]int{r.fn.newSlot(), r.fn.newSlot()}
		saved := r.sc
		r.sc = &scope{vars: map[string]*Var{}, parent: saved}
		r.decl[s] = r.declare(s.Var, false)
		r.block(s.Body)
		r.sc = saved
	case *ReturnStmt:
		if r.fn == r.main {
			compileErr(s.P, "return outside function")
		}
		if s.Value != nil {
			r.expr(s.Value)
		}
	case *BreakStmt, *ContinueStmt:
	default:
		compileErr(s.Pos(), "unsupported statement %T", s)
	}
}

func (r *Resolver) expr(e Expr) {
	switch e := e.(type) {
	case *NumberLit:
		if e.V != float64(int64(e.V)) {
			compileErr(e.P, "only integers are supported (got %v)", e.V)
		}
	case *StringLit, *BoolLit, *NilLit:
	case *Ident:
		r.use(e)
	case *ArrayLit:
		for _, x := range e.Elems {
			r.expr(x)
		}
	case *MapLit:
		for _, x := range e.Vals {
			r.expr(x)
		}
	case *Unary:
		r.expr(e.X)
	case *Binary:
		r.expr(e.L)
		r.expr(e.R)
	case *Assign:
		if id, ok := e.Target.(*Ident); ok {
			v := r.use(id)
			if v.Const || v.Builtin {
				compileErr(e.P, "cannot assign to const %s", id.Name)
			}
		} else {
			r.expr(e.Target)
		}
		r.expr(e.Value)
	case *Index:
		r.expr(e.X)
		r.expr(e.Key)
	case *Call:
		r.expr(e.Fn)
		if id, ok := e.Fn.(*Ident); (!ok || !r.uses[id].Builtin) && len(e.Args) > 7 {
			compileErr(e.P, "at most 7 arguments are supported")
		}
		for _, a := range e.Args {
			r.expr(a)
		}
	case *FuncLit:
		r.funcLit(e)
	default:
		compileErr(e.Pos(), "unsupported expression %T", e)
	}
}

func (r *Resolver) funcLit(f *FuncLit) {
	if len(f.Params) > 7 {
		compileErr(f.P, "at most 7 parameters are supported")
	}
	name := f.Name
	if name == "" {
		name = "<anon>"
	}
	fi := &FuncInfo{Lit: f, Name: name, Parent: r.fn}
	fi.Label = fmt.Sprintf("fn_%d", len(r.all))
	if f.Name != "" {
		fi.Label += "_" + f.Name
	}
	r.funcs[f] = fi
	r.all = append(r.all, fi)
	savedFn, savedSc := r.fn, r.sc
	r.fn = fi
	r.sc = &scope{vars: map[string]*Var{}, parent: savedSc}
	for _, p := range f.Params {
		fi.Params = append(fi.Params, r.declare(p, false))
	}
	r.stmts(f.Body)
	r.fn, r.sc = savedFn, savedSc
}
