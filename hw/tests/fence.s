# fence.s — fence は単一サイクル CPU では nop。ecall の戻り値 (a0) と未知の syscall
    .text
    .globl _start
_start:
    li t6, 1
    fence
    li a0, 5
    li a7, 1000                 # 存在しない syscall → -ENOSYS (-38)
    ecall
    li a1, -38
    bne a0, a1, fail

    li t6, 2                    # brk(0) は現在の位置、brk(x) で伸ばせる
    li a0, 0
    li a7, 214
    ecall
    mv s0, a0
    addi a0, s0, 256
    li a7, 214
    ecall
    addi a1, s0, 256
    bne a0, a1, fail
    sd a1, 0(s0)                # 伸ばしたヒープに書いて読める
    ld a0, 0(s0)
    bne a0, a1, fail

    la a1, msg
    li a2, 9
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
msg: .ascii "fence ok\n"
