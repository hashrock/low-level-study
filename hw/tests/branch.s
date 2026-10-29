# branch.s — 条件分岐 (成立 / 不成立、符号付き / 符号なし) と jal / jalr
    .text
    .globl _start
_start:
    li t0, -1
    li t1, 1

    li t6, 1
    beq t0, t0, .L1
    j fail
.L1: beq t0, t1, fail
    bne t0, t1, .L2
    j fail
.L2: bne t0, t0, fail

    li t6, 2                    # 符号付き: -1 < 1
    blt t0, t1, .L3
    j fail
.L3: blt t1, t0, fail
    bge t1, t0, .L4
    j fail
.L4: bge t0, t1, fail
    bge t0, t0, .L5             # 等しいときも bge は成立
    j fail
.L5:
    li t6, 3                    # 符号なし: 0xff..ff > 1
    bltu t1, t0, .L6
    j fail
.L6: bltu t0, t1, fail
    bgeu t0, t1, .L7
    j fail
.L7: bgeu t1, t0, fail

    li t6, 4                    # 後ろ向きの分岐 (ループ): 10 回まわす
    li t2, 10
    li a0, 0
.Lloop:
    addi a0, a0, 3
    addi t2, t2, -1
    bnez t2, .Lloop
    li a1, 30
    bne a0, a1, fail

    li t6, 5                    # jal: rd = 戻り番地 (pc + 4)
    jal ra, .Lsub
.Lret:
    la a1, .Lret
    bne a0, a1, fail

    li t6, 6                    # jalr: 飛び先の bit0 は 0 にされる
    la t0, .Ltarget
    addi t0, t0, 1              # 奇数番地にしても
    jalr ra, 0(t0)              # .Ltarget に着地する
    j fail
.Ltarget:
    li t6, 7                    # jalr のオフセット
    la t0, .Lback - 8
    jalr x0, 8(t0)
    j fail
.Lback:
    la a1, msg
    li a2, 10
    li a0, 1
    li a7, 64
    ecall
    li a0, 0
    li a7, 93
    ecall

.Lsub:
    mv a0, ra
    ret
fail:
    mv a0, t6
    li a7, 93
    ecall
    .data
msg: .ascii "branch ok\n"
