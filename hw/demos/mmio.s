# mmio.s — web/hw のデモ: MMIO コンソール (0x10000000 への sb が文字になる)
    .text
    .globl _start
_start:
    li   t0, 0x10000000         # コンソールの THR (送信レジスタ)
    la   t1, msg
.Lloop:
    lbu  t2, 0(t1)              # 文字列から 1 バイト (RAM)
    beqz t2, .Ldone
    sb   t2, 0(t0)              # コンソールへ 1 バイト (MMIO)
    addi t1, t1, 1
    j    .Lloop
.Ldone:
    li   a0, 0
    li   a7, 93
    ecall
    .data
msg:
    .asciz "Hi!\n"
