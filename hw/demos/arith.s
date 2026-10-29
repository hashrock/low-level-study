# arith.s — web/hw のデモ: 演算命令 (README の「波形を読む」の addi a0, a0, -1 = 0xfff50513)
    .text
    .globl _start
_start:
    li   a0, 5                  # addi a0, zero, 5
    addi a0, a0, -1             # 0xfff50513: a0 = 5 + (-1) = 4  (b_sel = IMM)
    li   a1, 3
    add  a2, a0, a1             # a2 = 4 + 3 = 7                 (b_sel = RS2)
    sub  a3, a0, a1             # a3 = 4 - 3 = 1                 (alu_op = SUB: bit30)
    slli a4, a2, 4              # a4 = 7 << 4 = 112
    lui  a5, 0x12345            # a5 = 0x12345000                (a_sel = ZERO)
    li   a7, 93                 # exit(a0)
    ecall
