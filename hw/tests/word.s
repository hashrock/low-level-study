# word.s — RV64 の *W 命令 (32 ビットで計算して符号拡張)
    .text
    .globl _start
_start:
    li t6, 1
    li t0, 0x7fffffff
    addiw a0, t0, 1             # 32 ビットで回り込み → 0x80000000 を符号拡張
    li a1, 0xffffffff80000000
    bne a0, a1, fail

    li t6, 2
    li t0, 0x100000005          # 上位 32 ビットは無視される
    li t1, 7
    subw a0, t0, t1             # 5 - 7 = -2
    li a1, -2
    bne a0, a1, fail
    addw a0, t0, t1
    li a1, 12
    bne a0, a1, fail

    li t6, 3
    li t0, 1
    slliw a0, t0, 31
    li a1, 0xffffffff80000000
    bne a0, a1, fail
    li t1, 33                   # sllw はシフト量の下位 5 ビットだけ使う
    sllw a0, t0, t1
    li a1, 2
    bne a0, a1, fail

    li t6, 4
    li t0, 0xffffffff80000000
    srliw a0, t0, 4             # 下位 32 ビット 0x80000000 >> 4 = 0x08000000
    li a1, 0x08000000
    bne a0, a1, fail
    sraiw a0, t0, 4             # 0xf8000000 → 符号拡張
    li a1, 0xfffffffff8000000
    bne a0, a1, fail
    li t1, 31
    srlw a0, t0, t1
    li a1, 1
    bne a0, a1, fail
    sraw a0, t0, t1
    li a1, -1
    bne a0, a1, fail

    li t6, 5                    # sext.w = addiw rd, rs, 0
    li t0, 0x12345678deadbeef
    sext.w a0, t0
    li a1, 0xffffffffdeadbeef
    bne a0, a1, fail

    la a1, msg
    li a2, 8
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
msg: .ascii "word ok\n"
