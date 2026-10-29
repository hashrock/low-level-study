# hazard.s — パイプライン版のハザード処理を狙ったテスト (単一サイクル版でもそのまま通る)
#   フォワーディング (EX/MEM から・MEM/WB から)、load-use ストール、分岐の直前の依存、
#   x0 への書き込みをフォワードしないこと、連続する ecall
    .text
    .globl _start
_start:
    la s0, buf

    li t6, 1                    # 連続する依存: 1 つ前 (EX/MEM) と 2 つ前 (MEM/WB) から
    li t0, 1
    addi t0, t0, 1              # t0 = 2  (EX/MEM から t0=1 をもらう)
    addi t0, t0, 1              # t0 = 3
    add  t1, t0, t0             # t1 = 6  (両方の入力がフォワード)
    nop
    add  t2, t1, t0             # t2 = 9  (t1 は MEM/WB から, t0 も MEM/WB の先 = レジスタ)
    li a1, 9
    bne t2, a1, fail

    li t6, 2                    # load-use: ロードの直後にその値を使う → 1 サイクル止まる
    li t0, 40
    sd t0, 0(s0)
    ld t1, 0(s0)
    addi t1, t1, 2              # ← ここで止まる
    li a1, 42
    bne t1, a1, fail

    li t6, 3                    # ロードした値をそのままストア (rs2 の load-use)
    ld t1, 0(s0)
    sd t1, 8(s0)
    ld t2, 8(s0)
    bne t2, t1, fail

    li t6, 4                    # ロード直後の分岐
    ld t1, 0(s0)
    li a1, 40
    bne t1, a1, fail

    li t6, 5                    # x0 への書き込みは次の命令にフォワードしてはいけない
    addi x0, x0, 7
    add  a0, x0, x0
    bnez a0, fail

    li t6, 6                    # 分岐成立の後ろの 2 命令は捨てられる (実行されてはいけない)
    li t0, 0
    j .Lskip
    addi t0, t0, 1
    addi t0, t0, 1
.Lskip:
    bnez t0, fail

    li t6, 7                    # jal の戻り番地 (ra) をすぐ使う
    jal ra, .Lf
.Lf:
    la a1, .Lf
    bne ra, a1, fail

    li t6, 8                    # ecall の戻り値 (a0) をすぐ使う、ecall が 2 つ続く
    li a0, 0
    li a7, 214                  # brk(0)
    ecall
    mv s1, a0
    li a7, 214
    ecall
    bne a0, s1, fail
    addi a0, a0, 0              # a0 を直後に使う
    beqz a0, fail

    li t6, 9                    # 乗除算の結果をすぐ使う
    li t0, 6
    li t1, 7
    mul t2, t0, t1
    divu t2, t2, t0
    bne t2, t1, fail

    la a1, msg
    li a2, 10
    li a0, 1
    li a7, 64
    ecall
    li a0, 0
    li a7, 93
    ecall
fail:
    mv a0, t6
    li a7, 93
    ecall
    .data
msg: .ascii "hazard ok\n"
    .bss
    .align 3
buf: .zero 16
