# runtime.s — minicc のランタイム (RV64IM, Linux システムコール)
#
# 値の表現 (64bit 1 語):
#   ...xxxx1  整数 n を 2n+1 で表す (63bit 整数)
#   2 / 4 / 6 nil / false / true
#   ...xx000  ヒープ object へのポインタ (8 バイト境界)。先頭語が型タグ
#
# object のレイアウト:
#   文字列   [1][長さ][バイト列...]
#   配列     [2][長さ][容量][要素バッファへのポインタ]
#   マップ   [3][キーの配列][値の配列]          (挿入順・線形探索)
#   関数     [4][コード][引数の数][名前][捕捉数][箱 0][箱 1]...
#   組込関数 [5][コード][引数の数][名前][0]
#   箱       [値]                              (捕捉された変数 1 個)
#
# 呼び出し規約は標準の RISC-V ABI (a0.. 引数/戻り値, t* は壊してよい, s* は保存)。
# 失敗しうる公開関数は入口で ra を rt_site に記録する。エラー時はそれを
# コンパイラが出力した位置表 mini_sites (戻り番地 → 行, 列) で引いて表示する。

	.equ SYS_WRITE, 64
	.equ SYS_EXIT, 93
	.equ NIL, 2
	.equ FALSE, 4
	.equ TRUE, 6
	.equ T_STR, 1
	.equ T_ARR, 2
	.equ T_MAP, 3
	.equ T_FN, 4
	.equ T_BUILTIN, 5
	.equ SB_CAP, 1048576

	.bss
	.balign 8
rt_heap:	.zero 67108864          # 64 MiB のヒープ (解放しないバンプ割り当て)
rt_heap_end:
rt_sb_buf:	.zero 1048576           # 出力・文字列組み立て用バッファ

	.data
	.balign 8
rt_hp:		.dword rt_heap          # 次に割り当てる位置
rt_sb_len:	.dword 0
rt_site:	.dword 0                # 最後に呼ばれた rt_* の戻り番地
	.globl rt_stack_limit
rt_stack_limit:	.dword 0                # sp がこれより下がったらスタックオーバーフロー

	.text
	.globl _start
_start:
	li fp, 0
	li t0, 8388608           # スタックは 8 MiB まで
	sub t0, sp, t0
	la t1, rt_stack_limit
	sd t0, 0(t1)
	call mini_main
	li a0, 0
	li a7, SYS_EXIT
	ecall

# ---------------- メモリ ----------------

# rt_alloc(a0=バイト数) -> a0=ポインタ (8 バイト境界, 0 埋め済み)
	.globl rt_alloc
rt_alloc:
	addi a0, a0, 7
	andi a0, a0, -8
	la t0, rt_hp
	ld t1, 0(t0)
	add t2, t1, a0
	la t3, rt_heap_end
	bgtu t2, t3, .Loom
	sd t2, 0(t0)
	mv a0, t1
	ret
.Loom:
	la a0, m_oom
	j rt_fail

# rt_box(a0=値) -> a0=箱
	.globl rt_box
rt_box:
	addi sp, sp, -16
	sd ra, 8(sp)
	sd a0, 0(sp)
	li a0, 8
	call rt_alloc
	ld t0, 0(sp)
	sd t0, 0(a0)
	ld ra, 8(sp)
	addi sp, sp, 16
	ret

# rt_closure_new(a0=捕捉数, a1=コード, a2=引数の数, a3=名前) -> a0=関数 object
	.globl rt_closure_new
rt_closure_new:
	addi sp, sp, -48
	sd ra, 40(sp)
	sd a0, 0(sp)
	sd a1, 8(sp)
	sd a2, 16(sp)
	sd a3, 24(sp)
	slli a0, a0, 3
	addi a0, a0, 40
	call rt_alloc
	li t0, T_FN
	sd t0, 0(a0)
	ld t0, 8(sp)
	sd t0, 8(a0)
	ld t0, 16(sp)
	sd t0, 16(a0)
	ld t0, 24(sp)
	sd t0, 24(a0)
	ld t0, 0(sp)
	sd t0, 32(a0)
	ld ra, 40(sp)
	addi sp, sp, 48
	ret

# _alloc_str(a0=長さ) -> a0=文字列 object (中身は未設定)
_alloc_str:
	addi sp, sp, -16
	sd ra, 8(sp)
	sd a0, 0(sp)
	addi a0, a0, 16
	call rt_alloc
	li t0, T_STR
	sd t0, 0(a0)
	ld t0, 0(sp)
	sd t0, 8(a0)
	ld ra, 8(sp)
	addi sp, sp, 16
	ret

# _memcpy(a0=dst, a1=src, a2=n) -> a0=dst+n
_memcpy:
	beqz a2, .Lmc_done
	lbu t0, 0(a1)
	sb t0, 0(a0)
	addi a0, a0, 1
	addi a1, a1, 1
	addi a2, a2, -1
	j _memcpy
.Lmc_done:
	ret

# ---------------- 文字列バッファ (sb) ----------------

# _sb_mem(a0=ptr, a1=len): バイト列を追加
_sb_mem:
	la t0, rt_sb_len
	ld t1, 0(t0)
	add t2, t1, a1
	li t3, SB_CAP
	bgtu t2, t3, .Lsb_full
	sd t2, 0(t0)
	la t3, rt_sb_buf
	add t3, t3, t1
.Lsbm_loop:
	beqz a1, .Lsbm_done
	lbu t4, 0(a0)
	sb t4, 0(t3)
	addi a0, a0, 1
	addi t3, t3, 1
	addi a1, a1, -1
	j .Lsbm_loop
.Lsbm_done:
	ret
.Lsb_full:
	la a0, m_sbfull
	j rt_fail

# _sb_putc(a0=バイト)
_sb_putc:
	la t0, rt_sb_len
	ld t1, 0(t0)
	li t2, SB_CAP
	bgeu t1, t2, .Lsb_full
	la t2, rt_sb_buf
	add t2, t2, t1
	sb a0, 0(t2)
	addi t1, t1, 1
	sd t1, 0(t0)
	ret

# _sb_str(a0=文字列 object)
_sb_str:
	ld a1, 8(a0)
	addi a0, a0, 16
	j _sb_mem

# _sb_cstr(a0=0 終端の文字列)
_sb_cstr:
	mv t0, a0
.Lcs_loop:
	lbu t1, 0(t0)
	beqz t1, .Lcs_end
	addi t0, t0, 1
	j .Lcs_loop
.Lcs_end:
	sub a1, t0, a0
	j _sb_mem

# _sb_int(a0=符号付き整数): 10 進で追加
_sb_int:
	addi sp, sp, -48
	sd ra, 40(sp)
	mv t0, a0
	addi t1, sp, 32          # 数字は sp+0..31 に後ろから書く
	mv t2, t1
	bgez t0, .Lint_pos
	neg t0, t0
.Lint_pos:
	li t3, 10
.Lint_loop:
	remu t4, t0, t3
	divu t0, t0, t3
	addi t4, t4, '0'
	addi t2, t2, -1
	sb t4, 0(t2)
	bnez t0, .Lint_loop
	bgez a0, .Lint_out
	li t4, '-'
	addi t2, t2, -1
	sb t4, 0(t2)
.Lint_out:
	mv a0, t2
	sub a1, t1, t2
	call _sb_mem
	ld ra, 40(sp)
	addi sp, sp, 48
	ret

# ---------------- 表示 ----------------

# _typename(a0=値) -> a0=型名の文字列 object
_typename:
	andi t0, a0, 1
	bnez t0, .Ltn_num
	li t0, NIL
	beq a0, t0, .Ltn_nil
	li t0, FALSE
	beq a0, t0, .Ltn_bool
	li t0, TRUE
	beq a0, t0, .Ltn_bool
	ld t0, 0(a0)
	li t1, T_STR
	beq t0, t1, .Ltn_str
	li t1, T_ARR
	beq t0, t1, .Ltn_arr
	li t1, T_MAP
	beq t0, t1, .Ltn_map
	la a0, tn_function
	ret
.Ltn_num:
	la a0, tn_number
	ret
.Ltn_nil:
	la a0, tn_nil
	ret
.Ltn_bool:
	la a0, tn_bool
	ret
.Ltn_str:
	la a0, tn_string
	ret
.Ltn_arr:
	la a0, tn_array
	ret
.Ltn_map:
	la a0, tn_map
	ret

# _fmt(a0=値, a1=quote): 値を sb に文字列化して追加。quote!=0 なら文字列を "..." で囲む
_fmt:
	addi sp, sp, -48
	sd ra, 40(sp)
	sd s1, 32(sp)
	sd s2, 24(sp)
	sd s3, 16(sp)
	sd s4, 8(sp)
	mv s1, a0
	mv s2, a1
	andi t0, a0, 1
	beqz t0, .Lf_notint
	srai a0, a0, 1
	call _sb_int
	j .Lf_ret
.Lf_notint:
	li t0, NIL
	la a0, cs_nil
	beq s1, t0, .Lf_cstr
	li t0, FALSE
	la a0, cs_false
	beq s1, t0, .Lf_cstr
	li t0, TRUE
	la a0, cs_true
	beq s1, t0, .Lf_cstr
	ld t0, 0(s1)
	li t1, T_STR
	beq t0, t1, .Lf_str
	li t1, T_ARR
	beq t0, t1, .Lf_arr
	li t1, T_MAP
	beq t0, t1, .Lf_map
	li t1, T_FN
	la a0, cs_fn
	beq t0, t1, .Lf_fn
	la a0, cs_builtin
.Lf_fn:                          # "<fn 名前>" / "<builtin 名前>"
	call _sb_cstr
	ld a0, 24(s1)
	call _sb_str
	li a0, '>'
	call _sb_putc
	j .Lf_ret
.Lf_cstr:
	call _sb_cstr
	j .Lf_ret
.Lf_str:
	mv a0, s1
	beqz s2, .Lf_cstr_obj
	li a0, '"'
	call _sb_putc
	ld s4, 8(s1)             # 残りバイト数
	addi s3, s1, 16          # 読む位置
.Lf_q_loop:
	beqz s4, .Lf_q_end
	lbu a0, 0(s3)
	li t0, '"'
	beq a0, t0, .Lf_q_esc
	li t0, '\\'
	beq a0, t0, .Lf_q_esc
	li t0, '\n'
	li t1, 'n'
	beq a0, t0, .Lf_q_esc2
	li t0, '\t'
	li t1, 't'
	beq a0, t0, .Lf_q_esc2
	call _sb_putc
	j .Lf_q_next
.Lf_q_esc:                       # \" と \\ : バックスラッシュ + その文字
	mv t1, a0
.Lf_q_esc2:
	mv s2, t1                # s2 はもう不要なので一時的に使う
	li a0, '\\'
	call _sb_putc
	mv a0, s2
	call _sb_putc
.Lf_q_next:
	addi s3, s3, 1
	addi s4, s4, -1
	j .Lf_q_loop
.Lf_q_end:
	li a0, '"'
	call _sb_putc
	j .Lf_ret
.Lf_cstr_obj:
	call _sb_str
	j .Lf_ret
.Lf_arr:                         # [e0, e1, ...]
	li a0, '['
	call _sb_putc
	li s3, 0
.Lf_a_loop:
	ld t0, 8(s1)
	bge s3, t0, .Lf_a_end
	beqz s3, .Lf_a_elem
	la a0, cs_comma
	call _sb_cstr
.Lf_a_elem:
	ld t0, 24(s1)
	slli t1, s3, 3
	add t0, t0, t1
	ld a0, 0(t0)
	li a1, 1
	call _fmt
	addi s3, s3, 1
	j .Lf_a_loop
.Lf_a_end:
	li a0, ']'
	call _sb_putc
	j .Lf_ret
.Lf_map:                         # {k0: v0, k1: v1}
	li a0, '{'
	call _sb_putc
	li s3, 0
.Lf_m_loop:
	ld t0, 8(s1)             # キー配列
	ld t1, 8(t0)
	bge s3, t1, .Lf_m_end
	beqz s3, .Lf_m_elem
	la a0, cs_comma
	call _sb_cstr
.Lf_m_elem:
	ld t0, 8(s1)
	ld t0, 24(t0)
	slli s4, s3, 3
	add t0, t0, s4
	ld a0, 0(t0)
	call _sb_str
	la a0, cs_colon
	call _sb_cstr
	ld t0, 16(s1)            # 値の配列
	ld t0, 24(t0)
	add t0, t0, s4
	ld a0, 0(t0)
	li a1, 1
	call _fmt
	addi s3, s3, 1
	j .Lf_m_loop
.Lf_m_end:
	li a0, '}'
	call _sb_putc
.Lf_ret:
	ld ra, 40(sp)
	ld s1, 32(sp)
	ld s2, 24(sp)
	ld s3, 16(sp)
	ld s4, 8(sp)
	addi sp, sp, 48
	ret

# print(a, b) は rt_print_val(a); rt_print_sp; rt_print_val(b); rt_print_nl に展開される
	.globl rt_print_val
rt_print_val:
	li a1, 0
	j _fmt
	.globl rt_print_sp
rt_print_sp:
	li a0, ' '
	j _sb_putc
	.globl rt_print_nl
rt_print_nl:                     # 改行を付けて write(1, buf, len) -> a0=nil
	addi sp, sp, -16
	sd ra, 8(sp)
	li a0, '\n'
	call _sb_putc
	li a0, 1
	la a1, rt_sb_buf
	la t0, rt_sb_len
	ld a2, 0(t0)
	sd zero, 0(t0)
	li a7, SYS_WRITE
	ecall
	li a0, NIL
	ld ra, 8(sp)
	addi sp, sp, 16
	ret

# rt_str(a0=値) -> 文字列 object (str() 組み込み関数)
	.globl rt_str
rt_str:
	addi sp, sp, -32
	sd ra, 24(sp)
	sd s1, 16(sp)
	sd s2, 8(sp)
	la t0, rt_sb_len
	ld s1, 0(t0)             # 現在の sb の長さ (後で戻す)
	li a1, 0
	call _fmt
	la t0, rt_sb_len
	ld a0, 0(t0)
	sub a0, a0, s1
	call _alloc_str
	mv s2, a0
	addi a0, a0, 16
	la a1, rt_sb_buf
	add a1, a1, s1
	ld a2, 8(s2)
	call _memcpy
	la t0, rt_sb_len
	sd s1, 0(t0)
	mv a0, s2
	ld ra, 24(sp)
	ld s1, 16(sp)
	ld s2, 8(sp)
	addi sp, sp, 32
	ret

# ---------------- エラー ----------------
# エラー経路は戻らないので s レジスタを保存せずに使ってよい。

# _err_begin: sb を空にして "行:列: runtime error: " を書く
_err_begin:
	addi sp, sp, -16
	sd ra, 8(sp)
	la t0, rt_sb_len
	sd zero, 0(t0)
	la t0, rt_site
	ld t1, 0(t0)
	la t2, mini_sites        # [戻り番地, 行, 列] の表 (0 で終わる)
.Lsite_loop:
	ld t3, 0(t2)
	beqz t3, .Lsite_none
	beq t3, t1, .Lsite_found
	addi t2, t2, 24
	j .Lsite_loop
.Lsite_found:
	sd t2, 0(sp)
	ld a0, 8(t2)
	call _sb_int
	li a0, ':'
	call _sb_putc
	ld t2, 0(sp)
	ld a0, 16(t2)
	call _sb_int
	li a0, ':'
	call _sb_putc
	li a0, ' '
	call _sb_putc
.Lsite_none:
	la a0, m_rterr
	call _sb_cstr
	ld ra, 8(sp)
	addi sp, sp, 16
	ret

# _err_end: 改行を付けて stderr に書き、exit(1)
_err_end:
	li a0, '\n'
	call _sb_putc
	li a0, 2
	la a1, rt_sb_buf
	la t0, rt_sb_len
	ld a2, 0(t0)
	li a7, SYS_WRITE
	ecall
	li a0, 1
	li a7, SYS_EXIT
	ecall

# rt_fail(a0=メッセージ (0 終端))
	.globl rt_fail
rt_fail:
	mv s1, a0
	call _err_begin
	mv a0, s1
	call _sb_cstr
	j _err_end

# 関数の入口のスタック検査に引っかかったとき (位置は不明なので rt_site を消す)
	.globl rt_stack_overflow
rt_stack_overflow:
	la t0, rt_site
	sd zero, 0(t0)
	la a0, m_stackov
	j rt_fail

# _fail_type(a0=メッセージ, a1=値): "メッセージ 型名"
_fail_type:
	mv s1, a0
	mv s2, a1
	call _err_begin
	mv a0, s1
	call _sb_cstr
	mv a0, s2
	call _typename
	call _sb_str
	j _err_end

# _binop_err(a0, a1, a2=演算子): "cannot apply + to string and number"
_binop_err:
	mv s1, a0
	mv s2, a1
	mv s3, a2
	call _err_begin
	la a0, m_apply
	call _sb_cstr
	mv a0, s3
	call _sb_cstr
	la a0, m_to
	call _sb_cstr
	mv a0, s1
	call _typename
	call _sb_str
	la a0, m_and
	call _sb_cstr
	mv a0, s2
	call _typename
	call _sb_str
	j _err_end

# ---------------- 演算 ----------------
# コンパイラは整数どうしの + - * < <= > >= をインラインで処理し、それ以外の場合だけここに来る。

	.globl rt_add
rt_add:
	la t6, rt_site
	sd ra, 0(t6)
	and t0, a0, a1
	andi t0, t0, 1
	beqz t0, .Ladd_str
	add a0, a0, a1
	addi a0, a0, -1
	ret
.Ladd_str:                       # 文字列どうしなら連結
	or t0, a0, a1
	andi t0, t0, 7
	bnez t0, .Ladd_err
	ld t0, 0(a0)
	ld t1, 0(a1)
	li t2, T_STR
	bne t0, t2, .Ladd_err
	bne t1, t2, .Ladd_err
	addi sp, sp, -32
	sd ra, 24(sp)
	sd s1, 16(sp)
	sd s2, 8(sp)
	sd s3, 0(sp)
	mv s1, a0
	mv s2, a1
	ld t0, 8(s1)
	ld t1, 8(s2)
	add a0, t0, t1
	call _alloc_str
	mv s3, a0
	addi a0, a0, 16
	addi a1, s1, 16
	ld a2, 8(s1)
	call _memcpy
	addi a1, s2, 16
	ld a2, 8(s2)
	call _memcpy
	mv a0, s3
	ld ra, 24(sp)
	ld s1, 16(sp)
	ld s2, 8(sp)
	ld s3, 0(sp)
	addi sp, sp, 32
	ret
.Ladd_err:
	la a2, op_add
	j _binop_err

# _intcheck: a0, a1 が両方整数でなければ a2 の演算子で型エラー
_intcheck:
	and t0, a0, a1
	andi t0, t0, 1
	beqz t0, _binop_err
	ret

	.globl rt_sub
rt_sub:
	la t6, rt_site
	sd ra, 0(t6)
	la a2, op_sub
	mv t5, ra
	call _intcheck
	sub a0, a0, a1
	addi a0, a0, 1
	jr t5

	.globl rt_mul
rt_mul:
	la t6, rt_site
	sd ra, 0(t6)
	la a2, op_mul
	mv t5, ra
	call _intcheck
	srai a1, a1, 1
	addi a0, a0, -1
	mul a0, a0, a1
	addi a0, a0, 1
	jr t5

	.globl rt_div
rt_div:
	la t6, rt_site
	sd ra, 0(t6)
	la a2, op_div
	mv t5, ra
	call _intcheck
	srai a0, a0, 1
	srai a1, a1, 1
	beqz a1, .Ldivzero
	div a0, a0, a1           # 整数除算 (0 方向に切り捨て)
	slli a0, a0, 1
	ori a0, a0, 1
	jr t5

	.globl rt_mod
rt_mod:
	la t6, rt_site
	sd ra, 0(t6)
	la a2, op_mod
	mv t5, ra
	call _intcheck
	srai a0, a0, 1
	srai a1, a1, 1
	beqz a1, .Ldivzero
	rem a0, a0, a1           # 符号は被除数に従う (Go の math.Mod と同じ)
	slli a0, a0, 1
	ori a0, a0, 1
	jr t5
.Ldivzero:
	la a0, m_divzero
	j rt_fail

	.globl rt_neg
rt_neg:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 1
	beqz t0, .Lneg_err
	li t0, 2
	sub a0, t0, a0           # -(2n+1)+2 = 2(-n)+1
	ret
.Lneg_err:
	mv a1, a0
	la a0, m_neg
	j _fail_type

# _strcmp(a0, a1: 文字列) -> a0 = -1 / 0 / 1 (辞書順)
_strcmp:
	ld t0, 8(a0)
	ld t1, 8(a1)
	addi a0, a0, 16
	addi a1, a1, 16
.Lsc_loop:
	beqz t0, .Lsc_aend
	beqz t1, .Lsc_gt
	lbu t2, 0(a0)
	lbu t3, 0(a1)
	bltu t2, t3, .Lsc_lt
	bltu t3, t2, .Lsc_gt
	addi a0, a0, 1
	addi a1, a1, 1
	addi t0, t0, -1
	addi t1, t1, -1
	j .Lsc_loop
.Lsc_aend:
	beqz t1, .Lsc_eq
.Lsc_lt:
	li a0, -1
	ret
.Lsc_gt:
	li a0, 1
	ret
.Lsc_eq:
	li a0, 0
	ret

# 比較の遅い道: 文字列どうしの辞書順比較。a3 = 0:< 1:<= 2:> 3:>=
	.globl rt_lt
rt_lt:
	la a2, op_lt
	li a3, 0
	j _cmp
	.globl rt_le
rt_le:
	la a2, op_le
	li a3, 1
	j _cmp
	.globl rt_gt
rt_gt:
	la a2, op_gt
	li a3, 2
	j _cmp
	.globl rt_ge
rt_ge:
	la a2, op_ge
	li a3, 3
_cmp:
	la t6, rt_site
	sd ra, 0(t6)
	or t0, a0, a1
	andi t0, t0, 7
	bnez t0, .Lcmp_int
	ld t0, 0(a0)
	ld t1, 0(a1)
	li t2, T_STR
	bne t0, t2, _binop_err
	bne t1, t2, _binop_err
	mv t5, ra
	mv t4, a3
	call _strcmp             # a0 = -1/0/1 (_strcmp は t4, t5 を使わない)
	mv ra, t5
	mv a3, t4
	j .Lcmp_res
.Lcmp_int:
	mv t5, ra
	call _intcheck
	mv ra, t5
	slt t0, a1, a0
	slt t1, a0, a1
	sub a0, t0, t1           # -1 / 0 / 1
.Lcmp_res:
	li t0, 1
	beqz a3, .Lc_lt
	beq a3, t0, .Lc_le
	li t0, 2
	beq a3, t0, .Lc_gt
	slti t0, a0, 0           # >= : !(c < 0)
	xori t0, t0, 1
	j .Lc_bool
.Lc_lt:
	slti t0, a0, 0
	j .Lc_bool
.Lc_le:
	slti t0, a0, 1
	j .Lc_bool
.Lc_gt:
	slt t0, zero, a0
.Lc_bool:
	slli t0, t0, 1
	addi a0, t0, FALSE
	ret

# rt_eq(a0, a1) -> TRUE/FALSE。同じ語なら等しい。文字列は中身を比較、それ以外は同一性
	.globl rt_eq
rt_eq:
	beq a0, a1, .Leq_true
	or t0, a0, a1
	andi t0, t0, 7
	bnez t0, .Leq_false
	ld t0, 0(a0)
	ld t1, 0(a1)
	li t2, T_STR
	bne t0, t2, .Leq_false
	bne t1, t2, .Leq_false
	mv t5, ra
	call _strcmp
	mv ra, t5
	bnez a0, .Leq_false
.Leq_true:
	li a0, TRUE
	ret
.Leq_false:
	li a0, FALSE
	ret

# ---------------- 関数呼び出し ----------------

# rt_checkcall(a0=呼ぶ値, a1=引数の数): 関数でなければ/引数の数が違えばエラー。a0 はそのまま返す
	.globl rt_checkcall
rt_checkcall:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 7
	bnez t0, .Lcc_notfn
	ld t0, 0(a0)
	li t1, T_FN
	beq t0, t1, .Lcc_arity
	li t1, T_BUILTIN
	bne t0, t1, .Lcc_notfn
.Lcc_arity:
	ld t0, 16(a0)
	bne t0, a1, .Lcc_badargs
	ret
.Lcc_notfn:
	mv a1, a0
	la a0, m_call
	j _fail_type
.Lcc_badargs:                    # "f expects 2 argument(s) but got 1"
	mv s1, a0
	mv s2, a1
	call _err_begin
	ld a0, 24(s1)
	call _sb_str
	la a0, m_expects
	call _sb_cstr
	ld a0, 16(s1)
	call _sb_int
	la a0, m_butgot
	call _sb_cstr
	mv a0, s2
	call _sb_int
	j _err_end

# ---------------- 配列 ----------------

# rt_array_new(a0=初期容量) -> 空の配列
	.globl rt_array_new
rt_array_new:
	addi sp, sp, -32
	sd ra, 24(sp)
	sd s1, 16(sp)
	sd s2, 8(sp)
	mv s1, a0                # 容量
	slli a0, a0, 3
	call rt_alloc
	mv s2, a0                # 要素バッファ
	li a0, 32
	call rt_alloc
	li t0, T_ARR
	sd t0, 0(a0)
	sd zero, 8(a0)
	sd s1, 16(a0)
	sd s2, 24(a0)
	ld ra, 24(sp)
	ld s1, 16(sp)
	ld s2, 8(sp)
	addi sp, sp, 32
	ret

# _apush(a0=配列, a1=値): 容量が足りなければ倍にして作り直す
_apush:
	ld t0, 8(a0)
	ld t1, 16(a0)
	blt t0, t1, .Lap_store
	addi sp, sp, -32
	sd ra, 24(sp)
	sd s1, 16(sp)
	sd s2, 8(sp)
	mv s1, a0
	mv s2, a1
	slli t1, t1, 1           # 新しい容量 = max(4, 容量*2)
	li t2, 4
	bge t1, t2, .Lap_cap
	mv t1, t2
.Lap_cap:
	sd t1, 16(s1)
	slli a0, t1, 3
	call rt_alloc
	ld a1, 24(s1)
	sd a0, 24(s1)
	ld a2, 8(s1)
	slli a2, a2, 3
	call _memcpy
	mv a0, s1
	mv a1, s2
	ld ra, 24(sp)
	ld s1, 16(sp)
	ld s2, 8(sp)
	addi sp, sp, 32
	ld t0, 8(a0)
.Lap_store:
	ld t2, 24(a0)
	slli t3, t0, 3
	add t2, t2, t3
	sd a1, 0(t2)
	addi t0, t0, 1
	sd t0, 8(a0)
	ret

# _isarr: a0 が配列でなければ a1 のメッセージで型エラー (a2 には触れない)
_isarr:
	andi t0, a0, 7
	bnez t0, .Lia_err
	ld t0, 0(a0)
	li t1, T_ARR
	bne t0, t1, .Lia_err
	ret
.Lia_err:
	mv t0, a0
	mv a0, a1
	mv a1, t0
	j _fail_type

# push(arr, v) -> nil
	.globl rt_push
rt_push:
	la t6, rt_site
	sd ra, 0(t6)
	addi sp, sp, -16
	sd ra, 8(sp)
	sd a1, 0(sp)
	la a1, m_push
	call _isarr
	ld a1, 0(sp)
	call _apush
	li a0, NIL
	ld ra, 8(sp)
	addi sp, sp, 16
	ret

# pop(arr) -> 最後の要素
	.globl rt_pop
rt_pop:
	la t6, rt_site
	sd ra, 0(t6)
	mv t5, ra
	la a1, m_pop
	call _isarr
	ld t0, 8(a0)
	beqz t0, .Lpop_empty
	addi t0, t0, -1
	sd t0, 8(a0)
	ld t1, 24(a0)
	slli t0, t0, 3
	add t1, t1, t0
	ld a0, 0(t1)
	jr t5
.Lpop_empty:
	la a0, m_popempty
	j rt_fail

# _copyarr(a0=配列) -> 新しい配列 (同じ要素)
_copyarr:
	addi sp, sp, -16
	sd ra, 8(sp)
	sd s1, 0(sp)
	mv s1, a0
	ld a0, 8(s1)
	call rt_array_new
	ld t0, 8(s1)
	sd t0, 8(a0)
	mv t1, a0
	ld a0, 24(t1)
	ld a1, 24(s1)
	slli a2, t0, 3
	mv s1, t1
	call _memcpy
	mv a0, s1
	ld ra, 8(sp)
	ld s1, 0(sp)
	addi sp, sp, 16
	ret

# range(from, to) -> [from, ..., to-1]
	.globl rt_range
rt_range:
	la t6, rt_site
	sd ra, 0(t6)
	la a2, m_range
	and t0, a0, a1
	andi t0, t0, 1
	beqz t0, rt_fail_a2
	addi sp, sp, -32
	sd ra, 24(sp)
	sd s1, 16(sp)
	sd s2, 8(sp)
	sd s3, 0(sp)
	mv s1, a0                # 現在値 (タグ付き)
	mv s2, a1                # 終端 (タグ付き)
	li a0, 0
	call rt_array_new
	mv s3, a0
.Lrange_loop:
	bge s1, s2, .Lrange_end
	mv a0, s3
	mv a1, s1
	call _apush
	addi s1, s1, 2           # タグ付きで +1
	j .Lrange_loop
.Lrange_end:
	mv a0, s3
	ld ra, 24(sp)
	ld s1, 16(sp)
	ld s2, 8(sp)
	ld s3, 0(sp)
	addi sp, sp, 32
	ret
rt_fail_a2:
	mv a0, a2
	j rt_fail

# len(x)
	.globl rt_len
rt_len:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 7
	bnez t0, .Llen_err
	ld t0, 0(a0)
	li t1, T_MAP
	bne t0, t1, .Llen_strarr
	ld a0, 8(a0)             # マップはキー配列の長さ
	ld t0, 0(a0)
.Llen_strarr:
	li t1, T_STR
	beq t0, t1, .Llen_ok
	li t1, T_ARR
	bne t0, t1, .Llen_err
.Llen_ok:
	ld a0, 8(a0)             # 文字列も配列も 8(a0) が長さ
	slli a0, a0, 1
	ori a0, a0, 1
	ret
.Llen_err:
	mv a1, a0
	la a0, m_len
	j _fail_type

# _toindex(a0=添字の値, a1=長さ) -> a0=生の添字。範囲外ならエラー
_toindex:
	andi t0, a0, 1
	beqz t0, .Lti_notnum
	srai a0, a0, 1
	bltz a0, .Lti_range
	bge a0, a1, .Lti_range
	ret
.Lti_notnum:
	mv a1, a0
	la a0, m_idxnum
	j _fail_type
.Lti_range:                      # "index 5 out of range (length 2)"
	mv s1, a0
	mv s2, a1
	call _err_begin
	la a0, m_index
	call _sb_cstr
	mv a0, s1
	call _sb_int
	la a0, m_oor
	call _sb_cstr
	mv a0, s2
	call _sb_int
	li a0, ')'
	call _sb_putc
	j _err_end

# ---------------- マップ ----------------

	.globl rt_map_new
rt_map_new:
	addi sp, sp, -16
	sd ra, 8(sp)
	sd s1, 0(sp)
	li a0, 24
	call rt_alloc
	mv s1, a0
	li t0, T_MAP
	sd t0, 0(s1)
	li a0, 4
	call rt_array_new
	sd a0, 8(s1)
	li a0, 4
	call rt_array_new
	sd a0, 16(s1)
	mv a0, s1
	ld ra, 8(sp)
	ld s1, 0(sp)
	addi sp, sp, 16
	ret

# _mapkey(a1=キー): 文字列でなければエラー
_mapkey:
	andi t0, a1, 7
	bnez t0, .Lmk_err
	ld t0, 0(a1)
	li t1, T_STR
	bne t0, t1, .Lmk_err
	ret
.Lmk_err:
	la a0, m_mapkey
	j _fail_type

# _map_find(a0=マップ, a1=キー文字列) -> a0=位置 (無ければ -1)
_map_find:
	addi sp, sp, -32
	sd ra, 24(sp)
	sd s1, 16(sp)
	sd s2, 8(sp)
	sd s3, 0(sp)
	ld s1, 8(a0)             # キー配列
	mv s2, a1
	li s3, 0
.Lmf_loop:
	ld t0, 8(s1)
	bge s3, t0, .Lmf_none
	ld t0, 24(s1)
	slli t1, s3, 3
	add t0, t0, t1
	ld a0, 0(t0)
	mv a1, s2
	call rt_eq
	li t0, TRUE
	beq a0, t0, .Lmf_found
	addi s3, s3, 1
	j .Lmf_loop
.Lmf_none:
	li s3, -1
.Lmf_found:
	mv a0, s3
	ld ra, 24(sp)
	ld s1, 16(sp)
	ld s2, 8(sp)
	ld s3, 0(sp)
	addi sp, sp, 32
	ret

# has(map, key) -> bool
	.globl rt_has
rt_has:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 7
	bnez t0, .Lhas_err
	ld t0, 0(a0)
	li t1, T_MAP
	bne t0, t1, .Lhas_err
	addi sp, sp, -16
	sd ra, 8(sp)
	call _mapkey
	call _map_find
	slti t0, a0, 0
	xori t0, t0, 1
	slli t0, t0, 1
	addi a0, t0, FALSE
	ld ra, 8(sp)
	addi sp, sp, 16
	ret
.Lhas_err:
	la a0, m_has
	j rt_fail

# keys(map) -> キーの配列 (コピー)
	.globl rt_keys
rt_keys:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 7
	bnez t0, .Lkeys_err
	ld t0, 0(a0)
	li t1, T_MAP
	bne t0, t1, .Lkeys_err
	ld a0, 8(a0)
	j _copyarr
.Lkeys_err:
	mv a1, a0
	la a0, m_keys
	j _fail_type

# rt_iter(a0) -> for-in で回す配列。配列はそのまま、マップはキーのコピー
	.globl rt_iter
rt_iter:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 7
	bnez t0, .Liter_err
	ld t0, 0(a0)
	li t1, T_ARR
	beq t0, t1, .Liter_ret
	li t1, T_MAP
	bne t0, t1, .Liter_err
	ld a0, 8(a0)
	j _copyarr
.Liter_ret:
	ret
.Liter_err:
	mv a1, a0
	la a0, m_iter
	j _fail_type

# rt_index(a0=obj, a1=key) -> obj[key]
	.globl rt_index
rt_index:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 7
	bnez t0, .Lix_err
	ld t0, 0(a0)
	li t1, T_ARR
	beq t0, t1, .Lix_arr
	li t1, T_STR
	beq t0, t1, .Lix_str
	li t1, T_MAP
	bne t0, t1, .Lix_err
	addi sp, sp, -32         # マップ
	sd ra, 24(sp)
	sd a0, 16(sp)
	sd a1, 8(sp)
	call _mapkey
	call _map_find
	bltz a0, .Lix_nokey
	ld t0, 16(sp)
	ld t0, 16(t0)            # 値の配列
	ld t0, 24(t0)
	slli a0, a0, 3
	add t0, t0, a0
	ld a0, 0(t0)
	ld ra, 24(sp)
	addi sp, sp, 32
	ret
.Lix_nokey:                      # key "b" not found in map (use has() to check)
	ld s1, 8(sp)
	call _err_begin
	la a0, m_key
	call _sb_cstr
	mv a0, s1
	li a1, 1
	call _fmt
	la a0, m_notfound
	call _sb_cstr
	j _err_end
.Lix_arr:
	mv t5, ra
	mv t4, a0
	mv a0, a1
	ld a1, 8(t4)
	call _toindex
	ld t0, 24(t4)
	slli a0, a0, 3
	add t0, t0, a0
	ld a0, 0(t0)
	jr t5
.Lix_str:                        # 1 バイトの文字列を作る
	addi sp, sp, -16
	sd ra, 8(sp)
	sd a0, 0(sp)
	mv a0, a1
	ld t0, 0(sp)
	ld a1, 8(t0)
	call _toindex
	ld t0, 0(sp)
	add t0, t0, a0
	lbu t0, 16(t0)
	sd t0, 0(sp)
	li a0, 1
	call _alloc_str
	ld t0, 0(sp)
	sb t0, 16(a0)
	ld ra, 8(sp)
	addi sp, sp, 16
	ret
.Lix_err:
	mv a1, a0
	la a0, m_cantindex
	j _fail_type

# rt_setindex(a0=obj, a1=key, a2=値) -> a0=値
	.globl rt_setindex
rt_setindex:
	la t6, rt_site
	sd ra, 0(t6)
	andi t0, a0, 7
	bnez t0, .Lsi_err
	ld t0, 0(a0)
	li t1, T_ARR
	beq t0, t1, .Lsi_arr
	li t1, T_MAP
	bne t0, t1, .Lsi_err
	addi sp, sp, -32
	sd ra, 24(sp)
	sd a0, 16(sp)
	sd a1, 8(sp)
	sd a2, 0(sp)
	call _mapkey
	call _map_find
	bltz a0, .Lsi_new
	ld t0, 16(sp)
	ld t0, 16(t0)
	ld t0, 24(t0)
	slli a0, a0, 3
	add t0, t0, a0
	ld a0, 0(sp)
	sd a0, 0(t0)
	j .Lsi_ret
.Lsi_new:                        # 新しいキー: キー配列と値の配列の両方に追加
	ld t0, 16(sp)
	ld a0, 8(t0)
	ld a1, 8(sp)
	call _apush
	ld t0, 16(sp)
	ld a0, 16(t0)
	ld a1, 0(sp)
	call _apush
	ld a0, 0(sp)
.Lsi_ret:
	ld ra, 24(sp)
	addi sp, sp, 32
	ret
.Lsi_arr:
	mv t5, ra
	mv t4, a0
	mv a0, a1
	ld a1, 8(t4)
	call _toindex
	ld t0, 24(t4)
	slli a0, a0, 3
	add t0, t0, a0
	sd a2, 0(t0)
	mv a0, a2
	jr t5
.Lsi_err:
	mv a1, a0
	la a0, m_cantassign
	j _fail_type

# type(v) -> 型名の文字列
	.globl rt_type
rt_type:
	j _typename

# ---------------- 値として使う組み込み関数 ----------------
# ユーザ関数と同じ規約 (a0=自分, a1..=引数) で呼ばれるので、引数をずらして本体へ。

	.data
	.balign 8
	.globl rt_fn_print
rt_fn_print:	.dword T_BUILTIN, .Lbw_print, 1, bn_print, 0
	.globl rt_fn_len
rt_fn_len:	.dword T_BUILTIN, .Lbw_len, 1, bn_len, 0
	.globl rt_fn_push
rt_fn_push:	.dword T_BUILTIN, .Lbw_push, 2, bn_push, 0
	.globl rt_fn_pop
rt_fn_pop:	.dword T_BUILTIN, .Lbw_pop, 1, bn_pop, 0
	.globl rt_fn_keys
rt_fn_keys:	.dword T_BUILTIN, .Lbw_keys, 1, bn_keys, 0
	.globl rt_fn_has
rt_fn_has:	.dword T_BUILTIN, .Lbw_has, 2, bn_has, 0
	.globl rt_fn_type
rt_fn_type:	.dword T_BUILTIN, .Lbw_type, 1, bn_type, 0
	.globl rt_fn_str
rt_fn_str:	.dword T_BUILTIN, .Lbw_str, 1, bn_str, 0
	.globl rt_fn_range
rt_fn_range:	.dword T_BUILTIN, .Lbw_range, 1, bn_range, 0

	.text
.Lbw_print:
	addi sp, sp, -16
	sd ra, 8(sp)
	mv a0, a1
	call rt_print_val
	call rt_print_nl
	ld ra, 8(sp)
	addi sp, sp, 16
	ret
.Lbw_len:
	mv a0, a1
	j rt_len
.Lbw_push:
	mv a0, a1
	mv a1, a2
	j rt_push
.Lbw_pop:
	mv a0, a1
	j rt_pop
.Lbw_keys:
	mv a0, a1
	j rt_keys
.Lbw_has:
	mv a0, a1
	mv a1, a2
	j rt_has
.Lbw_type:
	mv a0, a1
	j rt_type
.Lbw_str:
	mv a0, a1
	j rt_str
.Lbw_range:
	li a0, 1
	j rt_range

# ---------------- 定数データ ----------------

	.rodata
	.balign 8
tn_number:	.dword T_STR, 6
	.ascii "number"
	.balign 8
tn_string:	.dword T_STR, 6
	.ascii "string"
	.balign 8
tn_nil:		.dword T_STR, 3
	.ascii "nil"
	.balign 8
tn_bool:	.dword T_STR, 4
	.ascii "bool"
	.balign 8
tn_array:	.dword T_STR, 5
	.ascii "array"
	.balign 8
tn_map:		.dword T_STR, 3
	.ascii "map"
	.balign 8
tn_function:	.dword T_STR, 8
	.ascii "function"
	.balign 8
bn_print:	.dword T_STR, 5
	.ascii "print"
	.balign 8
bn_len:		.dword T_STR, 3
	.ascii "len"
	.balign 8
bn_push:	.dword T_STR, 4
	.ascii "push"
	.balign 8
bn_pop:		.dword T_STR, 3
	.ascii "pop"
	.balign 8
bn_keys:	.dword T_STR, 4
	.ascii "keys"
	.balign 8
bn_has:		.dword T_STR, 3
	.ascii "has"
	.balign 8
bn_type:	.dword T_STR, 4
	.ascii "type"
	.balign 8
bn_str:		.dword T_STR, 3
	.ascii "str"
	.balign 8
bn_range:	.dword T_STR, 5
	.ascii "range"

cs_nil:		.asciz "nil"
cs_true:	.asciz "true"
cs_false:	.asciz "false"
cs_fn:		.asciz "<fn "
cs_builtin:	.asciz "<builtin "
cs_comma:	.asciz ", "
cs_colon:	.asciz ": "
op_add:		.asciz "+"
op_sub:		.asciz "-"
op_mul:		.asciz "*"
op_div:		.asciz "/"
op_mod:		.asciz "%"
op_lt:		.asciz "<"
op_le:		.asciz "<="
op_gt:		.asciz ">"
op_ge:		.asciz ">="
m_rterr:	.asciz "runtime error: "
m_stackov:	.asciz "stack overflow (too deep recursion)"
m_oom:		.asciz "out of memory"
m_sbfull:	.asciz "string buffer overflow"
m_apply:	.asciz "cannot apply "
m_to:		.asciz " to "
m_and:		.asciz " and "
m_divzero:	.asciz "division by zero"
m_neg:		.asciz "cannot apply - to "
m_call:		.asciz "cannot call "
m_expects:	.asciz " expects "
m_butgot:	.asciz " argument(s) but got "
m_push:		.asciz "push: first argument must be an array, got "
m_pop:		.asciz "pop: argument must be an array, got "
m_popempty:	.asciz "pop: argument must be a non-empty array"
m_range:	.asciz "usage: range(n) or range(from, to) with numbers"
m_len:		.asciz "len: cannot take length of "
m_idxnum:	.asciz "index must be a number, got "
m_index:	.asciz "index "
m_oor:		.asciz " out of range (length "
m_mapkey:	.asciz "map key must be a string, got "
m_key:		.asciz "key "
m_notfound:	.asciz " not found in map (use has() to check)"
m_has:		.asciz "has: usage: has(map, string)"
m_keys:		.asciz "keys: argument must be a map, got "
m_iter:		.asciz "cannot iterate over "
m_cantindex:	.asciz "cannot index "
m_cantassign:	.asciz "cannot assign into "
