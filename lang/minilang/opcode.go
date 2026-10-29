package minilang

import (
	"fmt"
	"io"
	"strings"
)

// Op は 1 バイトの命令コード。オペランドはすべて 2 バイト (big endian) の符号なし整数。
type Op byte

const (
	OpConst     Op = iota // k        : 定数 k を積む
	OpNil                 //          : nil を積む
	OpTrue                //          : true を積む
	OpFalse               //          : false を積む
	OpPop                 //          : 1 つ捨てる
	OpGetLocal            // slot     : ローカル変数 (セル) の値を積む
	OpSetLocal            // slot     : スタックトップをローカルに代入 (pop しない)
	OpDefLocal            // slot     : pop した値で新しいセルを作り slot に置く
	OpGetFree             // i        : クロージャが捕捉した i 番目のセルの値を積む
	OpSetFree             // i        : 捕捉セルに代入 (pop しない)
	OpGetGlobal           // k(name)  : グローバル変数を積む
	OpSetGlobal           // k(name)  : グローバルに代入 (pop しない)
	OpDefGlobal           // k, const : グローバルを定義 (pop)
	OpAdd                 //          : a b -> a+b
	OpSub
	OpMul
	OpDiv
	OpMod
	OpEq
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
	OpNeg              // a -> -a
	OpNot              // a -> !a
	OpJump             // addr : 無条件ジャンプ
	OpJumpIfFalse      // addr : pop して偽ならジャンプ
	OpJumpIfFalseOrPop // addr : トップが偽ならそのままジャンプ、真なら pop (&&)
	OpJumpIfTrueOrPop  // addr : トップが真ならそのままジャンプ、偽なら pop (||)
	OpCall             // n    : f a1..an -> result
	OpReturn           //      : 戻り値を pop してフレームを抜ける
	OpClosure          // k    : 関数プロトタイプ k からクロージャを作る
	OpArray            // n    : n 個の値 -> 配列
	OpMap              // n    : k1 v1 .. kn vn -> マップ
	OpIndex            //      : obj key -> obj[key]
	OpSetIndex         //      : obj key val -> val (obj[key] = val)
	OpIterPrep         //      : 配列はそのまま、マップはキー配列に変換
	OpForIter          // c, i, addr : コレクション c の i 番目を積み i++、終わりなら addr へ
)

var opInfo = [...]struct {
	name string
	args int
}{
	OpConst: {"CONST", 1}, OpNil: {"NIL", 0}, OpTrue: {"TRUE", 0}, OpFalse: {"FALSE", 0},
	OpPop: {"POP", 0}, OpGetLocal: {"GET_LOCAL", 1}, OpSetLocal: {"SET_LOCAL", 1},
	OpDefLocal: {"DEF_LOCAL", 1}, OpGetFree: {"GET_FREE", 1}, OpSetFree: {"SET_FREE", 1},
	OpGetGlobal: {"GET_GLOBAL", 1}, OpSetGlobal: {"SET_GLOBAL", 1}, OpDefGlobal: {"DEF_GLOBAL", 2},
	OpAdd: {"ADD", 0}, OpSub: {"SUB", 0}, OpMul: {"MUL", 0}, OpDiv: {"DIV", 0}, OpMod: {"MOD", 0},
	OpEq: {"EQ", 0}, OpNe: {"NE", 0}, OpLt: {"LT", 0}, OpLe: {"LE", 0}, OpGt: {"GT", 0}, OpGe: {"GE", 0},
	OpNeg: {"NEG", 0}, OpNot: {"NOT", 0}, OpJump: {"JUMP", 1}, OpJumpIfFalse: {"JUMP_IF_FALSE", 1},
	OpJumpIfFalseOrPop: {"JUMP_IF_FALSE_OR_POP", 1}, OpJumpIfTrueOrPop: {"JUMP_IF_TRUE_OR_POP", 1},
	OpCall: {"CALL", 1}, OpReturn: {"RETURN", 0}, OpClosure: {"CLOSURE", 1},
	OpArray: {"ARRAY", 1}, OpMap: {"MAP", 1}, OpIndex: {"INDEX", 0}, OpSetIndex: {"SET_INDEX", 0},
	OpIterPrep: {"ITER_PREP", 0}, OpForIter: {"FOR_ITER", 3},
}

var binOps = map[string]Op{
	"+": OpAdd, "-": OpSub, "*": OpMul, "/": OpDiv, "%": OpMod,
	"==": OpEq, "!=": OpNe, "<": OpLt, "<=": OpLe, ">": OpGt, ">=": OpGe,
}

var opSymbol = map[Op]string{}

func init() {
	for s, op := range binOps {
		opSymbol[op] = s
	}
}

// Proto はコンパイル済みの関数 (バイトコード + 定数表)。
type Proto struct {
	Name      string
	NumParams int
	NumLocals int
	Code      []byte
	Pos       []Pos // Code と同じ長さ。各バイトのソース位置
	Consts    []Value
	Captures  []Capture

	// デバッグ情報 (ステップ実行の表示用)
	LocalNames []string // スロット番号 → 変数名 (" coll" " idx" は for-in の隠しローカル)
	FreeNames  []string // 捕捉セル番号 → 変数名
}

// Capture はクロージャ生成時にどのセルを捕捉するか。
type Capture struct {
	FromLocal bool // true: 外側関数のローカル slot / false: 外側関数の捕捉セル
	Index     int
}

func u16(code []byte, at int) int { return int(code[at])<<8 | int(code[at+1]) }

// Disassemble はバイトコードを人間が読める形で出力する (-dis オプション)。
func Disassemble(p *Proto, w io.Writer) {
	fmt.Fprintf(w, "== %s (params=%d locals=%d captures=%v) ==\n", protoName(p), p.NumParams, p.NumLocals, p.Captures)
	for ip := 0; ip < len(p.Code); {
		in := DecodeInstr(p, ip)
		line := fmt.Sprintf("%04d %6s  %-21s", ip, p.Pos[ip], in.Op)
		for _, a := range in.Args {
			line += fmt.Sprintf(" %d", a)
		}
		if in.Comment != "" {
			line += "    ; " + in.Comment
		}
		fmt.Fprintln(w, line)
		ip = in.Next
	}
	for _, c := range p.Consts {
		if sub, ok := c.(*Proto); ok {
			fmt.Fprintln(w)
			Disassemble(sub, w)
		}
	}
}

// Instr は 1 命令をデコードしたもの。
type Instr struct {
	IP      int    `json:"ip"`
	Op      string `json:"op"`
	Args    []int  `json:"args"`
	Comment string `json:"comment,omitempty"` // 定数の中身など
	Next    int    `json:"-"`                 // 次の命令の位置
}

// DecodeInstr は p の ip にある命令をデコードする。
func DecodeInstr(p *Proto, ip int) Instr {
	op := Op(p.Code[ip])
	in := Instr{IP: ip, Op: "?", Args: []int{}}
	if int(op) >= len(opInfo) || opInfo[op].name == "" {
		in.Next = ip + 1
		return in
	}
	info := opInfo[op]
	in.Op = info.name
	for k := 0; k < info.args; k++ {
		in.Args = append(in.Args, u16(p.Code, ip+1+2*k))
	}
	in.Next = ip + 1 + 2*info.args
	switch op {
	case OpConst, OpGetGlobal, OpSetGlobal, OpDefGlobal, OpClosure:
		in.Comment = format(p.Consts[in.Args[0]], true)
	case OpGetLocal, OpSetLocal, OpDefLocal:
		in.Comment = nameAt(p.LocalNames, in.Args[0])
	case OpGetFree, OpSetFree:
		in.Comment = nameAt(p.FreeNames, in.Args[0])
	case OpForIter:
		in.Comment = nameAt(p.LocalNames, in.Args[0]) + ", " + nameAt(p.LocalNames, in.Args[1])
	}
	return in
}

func nameAt(names []string, i int) string {
	if i < len(names) {
		return strings.TrimSpace(names[i])
	}
	return ""
}

// ProtoName は表示用の関数名 (無名関数は "<anon>")。
func ProtoName(p *Proto) string { return protoName(p) }

func protoName(p *Proto) string {
	if p.Name == "" {
		return "<anon>"
	}
	return p.Name
}
