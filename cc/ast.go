package main

// ---- 型 ----

type TypeKind int

const (
	TyVoid TypeKind = iota
	TyChar
	TyShort
	TyInt
	TyLong
	TyPtr
	TyArray
	TyStruct
	TyFunc
)

type Type struct {
	Kind  TypeKind
	Size  int // sizeof
	Align int
	Base  *Type // ポインタ・配列の要素型
	Len   int   // 配列の要素数 (-1 は [] で長さ未定)

	Members  []*Member // struct
	Complete bool      // struct の中身が定義済みか

	Ret        *Type // 関数
	Params     []*Type
	ParamNames []*Token // 関数定義で引数を変数にするため
	Variadic   bool
}

type Member struct {
	Name   string
	Ty     *Type
	Offset int
}

var (
	tyVoid  = &Type{Kind: TyVoid, Size: 1, Align: 1}
	tyChar  = &Type{Kind: TyChar, Size: 1, Align: 1}
	tyShort = &Type{Kind: TyShort, Size: 2, Align: 2}
	tyInt   = &Type{Kind: TyInt, Size: 4, Align: 4}
	tyLong  = &Type{Kind: TyLong, Size: 8, Align: 8}
)

func pointerTo(base *Type) *Type { return &Type{Kind: TyPtr, Size: 8, Align: 8, Base: base} }

func arrayOf(base *Type, n int) *Type {
	return &Type{Kind: TyArray, Size: base.Size * max(n, 0), Align: base.Align, Base: base, Len: n}
}

func funcType(ret *Type, params []*Type, variadic bool) *Type {
	return &Type{Kind: TyFunc, Size: 1, Align: 1, Ret: ret, Params: params, Variadic: variadic}
}

func (t *Type) isInteger() bool { return t.Kind >= TyChar && t.Kind <= TyLong }
func (t *Type) isPtr() bool     { return t.Kind == TyPtr || t.Kind == TyArray } // 添字・* が使える
func (t *Type) isScalar() bool  { return t.isInteger() || t.Kind == TyPtr }

func alignTo(n, a int) int { return (n + a - 1) / a * a }

// layoutStruct はメンバのオフセットと struct のサイズを決める。
// 各メンバは自分のアラインメントの倍数の位置に置き (間は詰め物)、
// 全体のサイズは最大アラインメントの倍数に切り上げる (配列にしたとき次の要素も揃うように)。
func layoutStruct(t *Type) {
	off, align := 0, 1
	for _, m := range t.Members {
		off = alignTo(off, m.Ty.Align)
		m.Offset = off
		off += m.Ty.Size
		align = max(align, m.Ty.Align)
	}
	t.Size, t.Align, t.Complete = alignTo(off, align), align, true
}

func (t *Type) String() string {
	switch t.Kind {
	case TyVoid:
		return "void"
	case TyChar:
		return "char"
	case TyShort:
		return "short"
	case TyInt:
		return "int"
	case TyLong:
		return "long"
	case TyPtr:
		return t.Base.String() + "*"
	case TyArray:
		return t.Base.String() + "[]"
	case TyStruct:
		return "struct"
	}
	return "function"
}

// ---- 変数・関数 ----

type Obj struct {
	Name     string
	Ty       *Type
	IsLocal  bool
	Offset   int // ローカル: fp からのオフセット (codegen が決める)
	IsStatic bool
	IsDef    bool // 関数: 本体あり / グローバル: 定義 (extern でない)

	// グローバル変数の初期値 (codegen が .data に書く)
	InitBytes []byte
	InitRels  []Reloc // InitBytes 中のポインタ (ラベル + 加数)

	// 関数
	Params []*Obj
	Locals []*Obj
	Body   *Node
	VaArea *Obj // 可変長引数関数で a0..a7 を退避する場所
}

type Reloc struct {
	Offset int
	Label  string
	Addend int64
}

// ---- 構文木 ----

type NodeKind int

const (
	// 式
	NNum      NodeKind = iota // Val
	NVar                      // Var
	NBinary                   // Op: + - * / % << >> & | ^ == != < <= > >= && ||
	NUnary                    // Op: - ! ~
	NAssign                   // Op: "=" または複合代入の演算子 ("+" など)
	NIncDec                   // Op: "+" / "-", Post: 後置か
	NAddr                     // &L
	NDeref                    // *L
	NMember                   // L.Name (-> は parser で (*L).Name にする)
	NCall                     // L(Args...)
	NCond                     // Cond ? Then : Else
	NComma                    // L, R
	NCast                     // (Ty)L
	NSizeof                   // sizeof L (check で NNum になる)
	NVaStart                  // __va_start()
	NStmtExpr                 // 初期化子を並べた式 (ローカル配列の初期化用)

	// 文
	NBlock // Body
	NExprStmt
	NIf
	NWhile
	NDoWhile
	NFor // Init; Cond; Inc
	NSwitch
	NCase // Val (Default なら default:)
	NReturn
	NBreak
	NContinue
)

type Node struct {
	Kind NodeKind
	Op   string
	Ty   *Type // check が付ける (式の型)
	Tok  *Token

	L, R             *Node
	Cond, Then, Else *Node
	Init, Inc        *Node
	Body             []*Node // ブロックの文 / 呼び出しの引数

	Val     int64
	Var     *Obj
	Name    string  // メンバ名
	Mem     *Member // check が解決したメンバ
	Post    bool
	Default bool
	Cases   []*Node // switch の case 一覧 (parser が集める)
	Label   int     // case / switch のラベル番号 (codegen)
}
