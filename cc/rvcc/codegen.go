package rvcc

import (
	"fmt"
	"strings"
)

// コード生成: 型の付いた木を RV64IM アセンブリにする。
//
// 基本は「スタックマシン」: 式の値は常に a0 に置き、二項演算の左辺のように
// 後で使う値はいったん「式スタック」に積む。最適化レベルで式スタックの実体と
// 細かい命令選択が変わる (README の「最適化」)。
//
//	-O0  式スタック = メモリ (addi sp / sd / ld)。変数は毎回 アドレス計算 → ld
//	-O1  + 葉 (定数・ローカル変数) はスタックを通さず直接レジスタへ、即値命令、fp 相対の直接 ld/sd、比較+分岐の融合
//	-O2  + 式スタックを callee-saved レジスタ s1..s11 に置く (関数呼び出しをまたいでも壊れない)
//
// レジスタの使い方:
//
//	a0      式の値 (アキュムレータ) / 戻り値 / 第 1 引数
//	a1-a7   引数
//	t0-t2   一時 (式スタックから降ろした値、即値、アドレス)
//	s0=fp   フレームポインタ
//	s1-s11  -O2 の式スタック (使った分だけ prologue で保存する)

type codegen struct {
	opt     int
	out     *strings.Builder
	fn      *Obj
	nlabel  int
	depth   int // 式スタックの深さ
	memPush int // そのうちメモリに積んだ数 (呼び出し時の 16 バイト整列用)
	maxS    int // 使った s レジスタの最大番号
	brk     []string
	cont    []string
	line    int
}

const numSRegs = 11

// Generate は翻訳単位全体のアセンブリを返す。
func Generate(prog *Program, opt int) string {
	var sb strings.Builder
	g := &codegen{opt: opt, out: &sb}
	g.data(prog)
	sb.WriteString("\n\t.text\n")
	for _, f := range prog.Globals {
		if f.Ty.Kind == TyFunc && f.IsDef {
			g.function(f)
		}
	}
	return sb.String()
}

func (g *codegen) emit(format string, args ...any) {
	fmt.Fprintf(g.out, "\t"+format+"\n", args...)
}

func (g *codegen) newLabel(kind string) string {
	g.nlabel++
	return fmt.Sprintf(".L%s%d", kind, g.nlabel)
}

func (g *codegen) label(l string) { fmt.Fprintf(g.out, "%s:\n", l) }

// ---- グローバル変数 ----

func (g *codegen) data(prog *Program) {
	for _, v := range prog.Globals {
		if v.Ty.Kind == TyFunc || !v.IsDef {
			continue
		}
		section := ".data"
		switch {
		case strings.HasPrefix(v.Name, ".Lstr"):
			section = ".rodata"
		case v.InitBytes == nil:
			section = ".bss"
		}
		fmt.Fprintf(g.out, "\n\t%s\n", section)
		if !v.IsStatic {
			g.emit(".globl %s", v.Name)
		}
		g.emit(".balign %d", v.Ty.Align)
		g.label(v.Name)
		switch {
		case section == ".bss":
			g.emit(".zero %d", v.Ty.Size)
		case section == ".rodata":
			g.emit(".asciz %s", quote(v.InitBytes[:len(v.InitBytes)-1]))
		default:
			g.initBytes(v)
		}
	}
}

func (g *codegen) initBytes(v *Obj) {
	b := v.InitBytes
	for i := 0; i < len(b); {
		if r := relocAt(v.InitRels, i); r != nil {
			if r.Addend != 0 {
				g.emit(".dword %s%+d", r.Label, r.Addend)
			} else {
				g.emit(".dword %s", r.Label)
			}
			i += 8
			continue
		}
		var parts []string
		for ; i < len(b) && len(parts) < 16 && relocAt(v.InitRels, i) == nil; i++ {
			parts = append(parts, fmt.Sprint(b[i]))
		}
		g.emit(".byte %s", strings.Join(parts, ", "))
	}
}

func relocAt(rs []Reloc, off int) *Reloc {
	for i := range rs {
		if rs[i].Offset == off {
			return &rs[i]
		}
	}
	return nil
}

func quote(b []byte) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, c := range b {
		switch {
		case c == '"' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c < 32 || c >= 127:
			fmt.Fprintf(&sb, `\x%02x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// ---- 関数 ----

// function はフレームを決めて本体を生成する。使う s レジスタの数は本体を作り終えるまで
// 分からないので、本体を別のバッファに出してから prologue を前に付ける。
//
//	        +------------------+ ← 呼び出し元の sp = fp
//	fp-8    | ra               |
//	fp-16   | 呼び出し元の fp   |
//	        | ローカル変数・引数 |  fp からの負のオフセット
//	        | ...              |
//	sp+8*k  | 保存した s1..sk   |
//	sp ───▶ +------------------+  (16 バイト境界)
func (g *codegen) function(fn *Obj) {
	g.fn, g.maxS, g.depth, g.memPush, g.line = fn, 0, 0, 0, 0
	off := 16
	for _, v := range fn.Locals {
		align := v.Ty.Align
		if v.Ty.Kind == TyArray || v.Ty.Kind == TyStruct {
			align = 8 // 0 埋めを 8 バイト単位でできるように
		}
		off = alignTo(off+v.Ty.Size, align)
		v.Offset = -off
	}
	localSize := off

	body := &strings.Builder{}
	outer := g.out
	g.out = body
	for i, p := range fn.Params {
		g.emit("%s a%d, %d(fp)%s", storeOp(p.Ty), i, p.Offset, comment("引数 "+p.Name))
	}
	if fn.VaArea != nil {
		for i := 0; i < 8; i++ {
			g.emit("sd a%d, %d(fp)%s", i, fn.VaArea.Offset+8*i, comment("可変長引数の退避場所"))
		}
	}
	g.stmt(fn.Body)
	g.out = outer
	ret := ".Lret_" + fn.Name
	text := strings.TrimSuffix(body.String(), "\tj "+ret+"\n") // 末尾の return は飛ぶ必要がない

	frame := alignTo(localSize+8*g.maxS, 16)
	fmt.Fprintf(g.out, "\n# ---- %s ----\n", fn.Name)
	if !fn.IsStatic {
		g.emit(".globl %s", fn.Name)
	}
	g.label(fn.Name)
	if frame < 2048 {
		g.emit("addi sp, sp, -%d%s", frame, comment("prologue: フレームを確保"))
		g.emit("sd ra, %d(sp)", frame-8)
		g.emit("sd fp, %d(sp)", frame-16)
		g.emit("addi fp, sp, %d", frame)
	} else {
		g.emit("addi sp, sp, -16%s", comment("prologue (大きなフレーム)"))
		g.emit("sd ra, 8(sp)")
		g.emit("sd fp, 0(sp)")
		g.emit("addi fp, sp, 16")
		g.emit("li t0, %d", frame-16)
		g.emit("sub sp, sp, t0")
	}
	for i := 1; i <= g.maxS; i++ {
		g.emit("sd s%d, %d(sp)%s", i, 8*(i-1), comment("callee-saved"))
	}
	g.out.WriteString(text)
	if fn.Name == "main" && text == body.String() {
		g.emit("li a0, 0%s", comment("main の末尾に達したら 0 を返す"))
	}
	g.label(ret)
	for i := 1; i <= g.maxS; i++ {
		g.emit("ld s%d, %d(sp)", i, 8*(i-1))
	}
	if frame < 2048 {
		g.emit("ld ra, %d(sp)%s", frame-8, comment("epilogue"))
		g.emit("ld fp, %d(sp)", frame-16)
		g.emit("addi sp, sp, %d", frame)
	} else {
		g.emit("addi sp, fp, -16%s", comment("epilogue"))
		g.emit("ld ra, 8(sp)")
		g.emit("ld fp, 0(sp)")
		g.emit("addi sp, sp, 16")
	}
	g.emit("ret")
}

func comment(s string) string { return "\t\t# " + s }

// ---- 式スタック ----

func (g *codegen) push() {
	if g.opt >= 2 && g.depth < numSRegs {
		g.depth++
		g.maxS = max(g.maxS, g.depth)
		g.emit("mv s%d, a0", g.depth)
		return
	}
	g.depth++
	g.memPush++
	g.emit("addi sp, sp, -8")
	g.emit("sd a0, 0(sp)")
}

// pop は積んだ値を取り出し、それが入っているレジスタ名を返す。
// dst を指定するとそのレジスタに移す (-O2 では s レジスタをそのまま返せば mv が要らない)。
func (g *codegen) pop(dst string) string {
	if g.opt >= 2 && g.depth <= numSRegs {
		s := fmt.Sprintf("s%d", g.depth)
		g.depth--
		if dst == "" {
			return s
		}
		g.emit("mv %s, %s", dst, s)
		return dst
	}
	if dst == "" {
		dst = "t1"
	}
	g.depth--
	g.memPush--
	g.emit("ld %s, 0(sp)", dst)
	g.emit("addi sp, sp, 8")
	return dst
}

// ---- ロード・ストア ----

func loadOp(t *Type) string {
	return map[int]string{1: "lb", 2: "lh", 4: "lw", 8: "ld"}[t.Size]
}

func storeOp(t *Type) string {
	return map[int]string{1: "sb", 2: "sh", 4: "sw", 8: "sd"}[t.Size]
}

// load は a0 が指す先の値を a0 に読む。配列・struct・関数は「アドレスそのものが値」なので何もしない。
func (g *codegen) load(t *Type) {
	if t.isScalar() {
		g.emit("%s a0, 0(a0)", loadOp(t))
	}
}

// store は a0 の値を addr が指す先に書く。struct は a0 が指す先からバイト列をコピーする。
func (g *codegen) store(t *Type, addr string) {
	if t.Kind != TyStruct {
		g.emit("%s a0, 0(%s)", storeOp(t), addr)
		return
	}
	chunk := min(t.Align, 8)
	ct := map[int]*Type{1: tyChar, 2: tyShort, 4: tyInt, 8: tyLong}[chunk]
	for i := 0; i < t.Size; i += chunk {
		g.emit("%s t0, %d(a0)", loadOp(ct), i)
		g.emit("%s t0, %d(%s)", storeOp(ct), i, addr)
	}
}

// directVar はローカル変数を fp 相対の 1 命令で読み書きできるか (-O1 以上)。
func (g *codegen) directVar(n *Node) bool {
	return g.opt >= 1 && n.Kind == NVar && n.Var.IsLocal && n.Ty.isScalar() &&
		n.Var.Offset >= -2048 && n.Var.Offset+8 < 2048
}

// isLeaf は式スタックを使わずに 1 命令でレジスタに置ける式か (定数・ローカル変数・グローバル配列/関数のアドレス)。
func (g *codegen) isLeaf(n *Node) bool {
	return g.opt >= 1 && (n.Kind == NNum || g.directVar(n) || isGlobalAddr(n))
}

func isGlobalAddr(n *Node) bool {
	return n.Kind == NVar && !n.Var.IsLocal && (n.Ty.Kind == TyArray || n.Ty.Kind == TyFunc)
}

func (g *codegen) loadLeaf(n *Node, reg string) {
	switch {
	case n.Kind == NNum:
		g.emit("li %s, %d", reg, n.Val)
	case isGlobalAddr(n):
		g.emit("la %s, %s", reg, n.Var.Name)
	default:
		g.emit("%s %s, %d(fp)%s", loadOp(n.Ty), reg, n.Var.Offset, comment(n.Var.Name))
	}
}

// genAddr は左辺値のアドレスを a0 に置く。
func (g *codegen) genAddr(n *Node) {
	switch n.Kind {
	case NVar:
		if n.Var.IsLocal {
			if n.Var.Offset >= -2048 {
				g.emit("addi a0, fp, %d%s", n.Var.Offset, comment("&"+n.Var.Name))
			} else {
				g.emit("li t0, %d", n.Var.Offset)
				g.emit("add a0, fp, t0%s", comment("&"+n.Var.Name))
			}
		} else {
			g.emit("la a0, %s", n.Var.Name)
		}
	case NDeref:
		g.expr(n.L)
	case NMember:
		g.genAddr(n.L)
		if n.Mem.Offset != 0 {
			g.emit("addi a0, a0, %d%s", n.Mem.Offset, comment("."+n.Mem.Name))
		}
	case NComma:
		g.expr(n.L)
		g.genAddr(n.R)
	default:
		panic(fmt.Sprintf("not an lvalue: %v", n.Kind))
	}
}

// cast は reg の値を from 型から to 型へ変換する。
// レジスタ上の整数は常に「型の幅で符号拡張された 64bit」にしておくので、縮めるときだけ命令が要る。
func (g *codegen) cast(reg string, from, to *Type) {
	if !to.isInteger() || to.Size == 8 || from.isInteger() && from.Size <= to.Size {
		return
	}
	switch to.Size {
	case 4:
		g.emit("sext.w %s, %s%s", reg, reg, comment("(int)"))
	default:
		sh := 64 - 8*to.Size
		g.emit("slli %s, %s, %d", reg, reg, sh)
		g.emit("srai %s, %s, %d%s", reg, reg, sh, comment("("+to.String()+")"))
	}
}

// ---- 式 ----

func (g *codegen) expr(n *Node) {
	switch n.Kind {
	case NNum:
		g.emit("li a0, %d", n.Val)
	case NVar:
		if g.directVar(n) {
			g.loadLeaf(n, "a0")
			return
		}
		if n.Ty.Kind == TyFunc {
			g.emit("la a0, %s", n.Var.Name)
			return
		}
		g.genAddr(n)
		g.load(n.Ty)
	case NMember, NDeref:
		g.genAddr(n)
		g.load(n.Ty)
	case NAddr:
		g.genAddr(n.L)
	case NVaStart:
		g.emit("addi a0, fp, %d%s", n.Var.Offset+8*len(g.fn.Params), comment("最初の可変長引数のアドレス"))
	case NCast:
		g.expr(n.L)
		g.cast("a0", n.L.Ty, n.Ty)
	case NComma:
		g.expr(n.L)
		g.expr(n.R)
	case NUnary:
		g.expr(n.L)
		switch n.Op {
		case "-":
			g.emit("neg%s a0, a0", w(n.Ty))
		case "~":
			g.emit("not a0, a0")
		case "!":
			g.emit("seqz a0, a0")
		}
	case NCond:
		els, end := g.newLabel("else"), g.newLabel("end")
		g.branchIfFalse(n.Cond, els)
		g.expr(n.Then)
		g.emit("j %s", end)
		g.label(els)
		g.expr(n.Else)
		g.label(end)
	case NAssign:
		g.assign(n)
	case NIncDec:
		g.incDec(n)
	case NCall:
		g.call(n)
	case NBinary:
		g.binary(n)
	default:
		panic(fmt.Sprintf("codegen: unexpected node %v", n.Kind))
	}
}

// w は 32bit 演算 (addw など) を使うかどうかの接尾辞。int の演算結果は 32bit で丸めて符号拡張する。
func w(t *Type) string {
	if t.Size == 4 {
		return "w"
	}
	return ""
}

func (g *codegen) binary(n *Node) {
	if n.Op == "&&" || n.Op == "||" { // 短絡評価
		end, short := g.newLabel("end"), g.newLabel("short")
		g.expr(n.L)
		if n.Op == "&&" {
			g.emit("beqz a0, %s", short)
		} else {
			g.emit("bnez a0, %s", short)
		}
		g.expr(n.R)
		g.emit("snez a0, a0")
		g.emit("j %s", end)
		g.label(short)
		g.emit("li a0, %d", b2i(n.Op == "||"))
		g.label(end)
		return
	}
	if v, label, err := evalConst(n); g.opt >= 1 && err == nil && label == "" { // 定数畳み込み
		g.emit("li a0, %d", v)
		return
	}
	lhs, rhs := g.operands(n)
	if rhs == "imm" {
		g.binaryImm(n, n.R.Val)
		return
	}
	g.op(n, "a0", lhs, rhs)
}

// operands は二項演算の両辺を評価し、それぞれが入っているレジスタを返す。
// -O0 では「左を積む → 右を a0 に → 左を降ろす」。-O1 以上で右辺が葉なら積まずに済む。
func (g *codegen) operands(n *Node) (string, string) {
	if g.isLeaf(n.R) {
		g.expr(n.L)
		if n.R.Kind == NNum && immOK(n.Op, n.R.Val) {
			return "a0", "imm"
		}
		if n.R.Kind == NNum && n.R.Val == 0 {
			return "a0", "zero"
		}
		g.loadLeaf(n.R, "t1")
		return "a0", "t1"
	}
	g.expr(n.L)
	g.push()
	g.expr(n.R)
	return g.pop(""), "a0"
}

func immOK(op string, v int64) bool {
	switch op {
	case "+", "&", "|", "^", "<":
		return v >= -2048 && v < 2048
	case "-":
		return v > -2048 && v <= 2048
	case "<<", ">>":
		return v >= 0 && v < 32
	}
	return false
}

func (g *codegen) binaryImm(n *Node, v int64) {
	ins := map[string]string{"+": "addi", "-": "addi", "&": "andi", "|": "ori", "^": "xori",
		"<<": "slli", ">>": "srai", "<": "slti"}[n.Op]
	if n.Op == "-" {
		v = -v
	}
	if n.Op == "+" || n.Op == "-" || n.Op == "<<" || n.Op == ">>" {
		ins += w(n.Ty)
	}
	g.emit("%s a0, a0, %d", ins, v)
}

// op は dst = x <op> y を出す。比較は 0/1 を作る。
func (g *codegen) op(n *Node, dst, x, y string) {
	t := n.Ty
	switch n.Op {
	case "+", "-", "*", "/", "%", "<<", ">>":
		ins := map[string]string{"+": "add", "-": "sub", "*": "mul", "/": "div", "%": "rem", "<<": "sll", ">>": "sra"}[n.Op]
		g.emit("%s%s %s, %s, %s", ins, w(t), dst, x, y)
	case "&", "|", "^":
		ins := map[string]string{"&": "and", "|": "or", "^": "xor"}[n.Op]
		g.emit("%s %s, %s, %s", ins, dst, x, y)
	case "==", "!=":
		g.emit("xor %s, %s, %s", dst, x, y)
		if n.Op == "==" {
			g.emit("seqz %s, %s", dst, dst)
		} else {
			g.emit("snez %s, %s", dst, dst)
		}
	case "<", ">", "<=", ">=":
		slt := "slt"
		if n.L.Ty.isPtr() {
			slt = "sltu" // アドレスは符号なしで比べる
		}
		if n.Op == ">" || n.Op == "<=" {
			x, y = y, x
		}
		g.emit("%s %s, %s, %s", slt, dst, x, y)
		if n.Op == "<=" || n.Op == ">=" {
			g.emit("xori %s, %s, 1", dst, dst)
		}
	}
}

// branchIfFalse は「cond が偽なら label へ」。-O1 以上では比較と分岐を 1 命令にまとめる。
func (g *codegen) branchIfFalse(cond *Node, label string) {
	inv := map[string]string{"==": "bne", "!=": "beq", "<": "bge", "<=": "bgt", ">": "ble", ">=": "blt"}
	if b := inv[cond.Op]; g.opt >= 1 && cond.Kind == NBinary && b != "" {
		x, y := g.operands(cond)
		if y == "imm" {
			g.loadLeaf(cond.R, "t1")
			y = "t1"
		}
		if cond.L.Ty.isPtr() && b != "bne" && b != "beq" {
			b += "u"
		}
		g.emit("%s %s, %s, %s", b, x, y, label)
		return
	}
	g.expr(cond)
	g.emit("beqz a0, %s", label)
}

func (g *codegen) assign(n *Node) {
	t := n.L.Ty
	if n.R == nil { // 0 埋め
		g.genAddr(n.L)
		for i := 0; i < t.Size; {
			if t.Size-i >= 8 {
				g.emit("sd zero, %d(a0)", i)
				i += 8
			} else {
				g.emit("sb zero, %d(a0)", i)
				i++
			}
		}
		return
	}
	if g.directVar(n.L) { // x = e / x += e を fp 相対で
		if n.Op == "=" {
			g.expr(n.R)
		} else {
			bin := &Node{Kind: NBinary, Op: n.Op, L: n.L, R: n.R, Ty: arith(t, n.R.Ty), Tok: n.Tok}
			g.binary(bin)
			g.cast("a0", bin.Ty, t)
		}
		g.emit("%s a0, %d(fp)%s", storeOp(t), n.L.Var.Offset, comment(n.L.Var.Name))
		return
	}
	g.genAddr(n.L)
	g.push()
	if n.Op != "=" {
		g.load(t)
		g.push()
		g.expr(n.R)
		old := g.pop("")
		bin := &Node{Kind: NBinary, Op: n.Op, L: n.L, R: n.R, Ty: arith(t, n.R.Ty)}
		g.op(bin, "a0", old, "a0")
		g.cast("a0", bin.Ty, t)
	} else {
		g.expr(n.R)
	}
	g.store(t, g.pop(""))
}

// incDec は ++x / x++ / --x / x--。後置なら式の値は変更前の値。
func (g *codegen) incDec(n *Node) {
	t := n.Ty
	step := n.Val
	if n.Op == "-" {
		step = -step
	}
	add := "addi" + w(t)
	if g.directVar(n.L) {
		g.loadLeaf(n.L, "a0")
		if n.Post {
			g.emit("%s t2, a0, %d", add, step)
			g.emit("%s t2, %d(fp)%s", storeOp(t), n.L.Var.Offset, comment(n.L.Var.Name))
		} else {
			g.emit("%s a0, a0, %d", add, step)
			g.cast("a0", tyLong, t)
			g.emit("%s a0, %d(fp)%s", storeOp(t), n.L.Var.Offset, comment(n.L.Var.Name))
		}
		return
	}
	g.genAddr(n.L)
	g.push()
	g.load(t)
	addr := g.pop("")
	if n.Post {
		g.emit("%s t2, a0, %d", add, step)
		g.emit("%s t2, 0(%s)", storeOp(t), addr)
	} else {
		g.emit("%s a0, a0, %d", add, step)
		g.cast("a0", tyLong, t)
		g.emit("%s a0, 0(%s)", storeOp(t), addr)
	}
}

// call は引数を評価して a0..a7 に入れ、call (関数名) か jalr (関数ポインタ) で呼ぶ。
// 引数を評価する途中で別の呼び出しがあると a レジスタが壊れるので、いったん式スタックに積む。
// -O1 以上では、葉の引数と最後の非葉の引数は積まずに直接 a レジスタへ入れる。
func (g *codegen) call(n *Node) {
	direct := n.L.Kind == NVar && n.L.Ty.Kind == TyFunc
	if !direct {
		g.expr(n.L)
		g.push()
	}
	last := -1 // 積まずに済む最後の非葉の引数
	if g.opt >= 1 {
		for i, a := range n.Body {
			if !g.isLeaf(a) {
				last = i
			}
		}
	}
	var pushed []int
	for i, a := range n.Body {
		switch {
		case g.isLeaf(a):
		case i == last:
			g.expr(a)
			if i != 0 {
				g.emit("mv a%d, a0", i)
			}
		default:
			g.expr(a)
			g.push()
			pushed = append(pushed, i)
		}
	}
	for j := len(pushed) - 1; j >= 0; j-- {
		g.pop(fmt.Sprintf("a%d", pushed[j]))
	}
	for i, a := range n.Body {
		if g.isLeaf(a) {
			g.loadLeaf(a, fmt.Sprintf("a%d", i))
		}
	}
	if !direct {
		g.pop("t0")
	}
	pad := g.memPush%2 == 1 // ABI: 呼び出し時の sp は 16 バイト境界
	if pad {
		g.emit("addi sp, sp, -8")
	}
	if direct {
		g.emit("call %s", n.L.Var.Name)
	} else {
		g.emit("jalr t0")
	}
	if pad {
		g.emit("addi sp, sp, 8")
	}
}

// ---- 文 ----

func (g *codegen) lineComment(n *Node) {
	if n.Tok == nil || n.Tok.Line == g.line || n.Tok.Line > len(n.Tok.File.Lines) {
		return
	}
	g.line = n.Tok.Line
	fmt.Fprintf(g.out, "\t# %d: %s\n", g.line, strings.TrimSpace(n.Tok.File.Lines[g.line-1]))
}

func (g *codegen) stmt(n *Node) {
	if n == nil {
		return
	}
	if n.Kind != NBlock {
		g.lineComment(n)
	}
	switch n.Kind {
	case NBlock:
		for _, s := range n.Body {
			g.stmt(s)
		}
	case NExprStmt:
		g.expr(n.L)
	case NReturn:
		if n.L != nil {
			g.expr(n.L)
		}
		g.emit("j .Lret_%s", g.fn.Name)
	case NIf:
		els, end := g.newLabel("else"), g.newLabel("end")
		g.branchIfFalse(n.Cond, els)
		g.stmt(n.Then)
		if n.Else != nil {
			g.emit("j %s", end)
		}
		g.label(els)
		if n.Else != nil {
			g.stmt(n.Else)
			g.label(end)
		}
	case NWhile, NFor:
		g.stmt(n.Init)
		begin, cont, end := g.newLabel("loop"), g.newLabel("cont"), g.newLabel("break")
		g.label(begin)
		if n.Cond != nil {
			g.branchIfFalse(n.Cond, end)
		}
		g.loop(n.Then, end, cont)
		g.label(cont)
		if n.Inc != nil {
			g.lineComment(n)
			g.expr(n.Inc)
		}
		g.emit("j %s", begin)
		g.label(end)
	case NDoWhile:
		begin, cont, end := g.newLabel("loop"), g.newLabel("cont"), g.newLabel("break")
		g.label(begin)
		g.loop(n.Then, end, cont)
		g.label(cont)
		g.expr(n.Cond)
		g.emit("bnez a0, %s", begin)
		g.label(end)
	case NSwitch:
		g.expr(n.Cond)
		end := g.newLabel("break")
		dflt := end
		for _, c := range n.Cases {
			c.Label = g.nlabel + 1
			l := g.newLabel("case")
			if c.Default {
				dflt = l
				continue
			}
			g.emit("li t1, %d", c.Val)
			g.emit("beq a0, t1, %s", l)
		}
		g.emit("j %s", dflt)
		g.brk = append(g.brk, end)
		g.stmt(n.Then)
		g.brk = g.brk[:len(g.brk)-1]
		g.label(end)
	case NCase:
		g.label(fmt.Sprintf(".Lcase%d", n.Label))
	case NBreak:
		g.emit("j %s", g.brk[len(g.brk)-1])
	case NContinue:
		g.emit("j %s", g.cont[len(g.cont)-1])
	}
}

func (g *codegen) loop(body *Node, brk, cont string) {
	g.brk = append(g.brk, brk)
	g.cont = append(g.cont, cont)
	g.stmt(body)
	g.brk = g.brk[:len(g.brk)-1]
	g.cont = g.cont[:len(g.cont)-1]
}
