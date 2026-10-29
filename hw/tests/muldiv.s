# muldiv.s — M 拡張 (乗算・除算・剰余)。0 除算とオーバーフローの決まりごとも
    .text
    .globl _start
_start:
    li t6, 1
    li t0, -7
    li t1, 3
    mul a0, t0, t1
    li a1, -21
    bne a0, a1, fail

    li t6, 2                    # 上位 64 ビット
    li t0, 0x8000000000000000   # 最小値 (-2^63)
    li t1, 2
    mulh a0, t0, t1             # -2^64 の上位 = -1
    li a1, -1
    bne a0, a1, fail
    mulhu a0, t0, t1            # 2^64 の上位 = 1
    li a1, 1
    bne a0, a1, fail
    li t2, -1
    mulhsu a0, t2, t1           # (-1) × 2 (符号なし) = -2 → 上位 = -1
    li a1, -1
    bne a0, a1, fail
    mulhu a0, t2, t2            # (2^64-1)^2 の上位 = 2^64 - 2
    li a1, -2
    bne a0, a1, fail

    li t6, 3                    # 符号付き除算は 0 方向に切り捨て
    li t0, -7
    li t1, 2
    div a0, t0, t1
    li a1, -3
    bne a0, a1, fail
    rem a0, t0, t1              # 余りは被除数と同じ符号
    li a1, -1
    bne a0, a1, fail
    divu a0, t0, t1
    li a1, 0x7ffffffffffffffc
    bne a0, a1, fail
    remu a0, t0, t1
    li a1, 1
    bne a0, a1, fail

    li t6, 4                    # 0 除算は例外にならない: 商 = -1, 余り = 被除数
    li t0, 42
    div a0, t0, x0
    li a1, -1
    bne a0, a1, fail
    divu a0, t0, x0
    bne a0, a1, fail
    rem a0, t0, x0
    bne a0, t0, fail
    remu a0, t0, x0
    bne a0, t0, fail

    li t6, 5                    # オーバーフロー: 最小値 / -1 = 最小値, 余り 0
    li t0, 0x8000000000000000
    li t1, -1
    div a0, t0, t1
    bne a0, t0, fail
    rem a0, t0, t1
    bne a0, x0, fail

    li t6, 6                    # *W 版
    li t0, 0x10000
    mulw a0, t0, t0             # 2^32 → 下位 32 ビットは 0
    bne a0, x0, fail
    li t0, 0x12345678
    li t1, 0x100
    mulw a0, t0, t1             # 0x34567800 (正)
    li a1, 0x34567800
    bne a0, a1, fail
    li t0, -7
    li t1, 2
    divw a0, t0, t1
    li a1, -3
    bne a0, a1, fail
    remw a0, t0, t1
    li a1, -1
    bne a0, a1, fail
    divuw a0, t0, t1            # 0xfffffff9 / 2 = 0x7ffffffc
    li a1, 0x7ffffffc
    bne a0, a1, fail
    remuw a0, t0, t1
    li a1, 1
    bne a0, a1, fail
    li t0, 0xffffffff80000000
    li t1, -1
    divw a0, t0, t1             # 32 ビットのオーバーフロー
    bne a0, t0, fail
    remw a0, t0, t1
    bne a0, x0, fail
    divuw a0, t0, x0            # 32 ビットの 0 除算
    li a1, -1
    bne a0, a1, fail
    remuw a0, t0, x0
    bne a0, t0, fail

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
msg: .ascii "muldiv ok\n"
