# mmio.s — システムコールを使わず、MMIO コンソール (UART 16550 の THR) に直接 1 バイトずつ書く
#   0x10000000 (THR) に sb すると 1 文字出る。書く前に LSR (+5) の bit5 (送信バッファ空) を確かめる
    .equ UART, 0x10000000
    .text
    .globl _start
_start:
    li s0, UART
    la s1, msg
.Lnext:
    lbu t0, 0(s1)
    beqz t0, .Ldone
.Lwait:
    lbu t1, 5(s0)               # LSR を読む (バスがコンソールに振り分ける)
    andi t1, t1, 0x20
    beqz t1, .Lwait
    sb t0, 0(s0)                # THR に書く → 1 文字出力
    addi s1, s1, 1
    j .Lnext
.Ldone:
    li a0, 0
    li a7, 93
    ecall
    .data
msg: .asciz "mmio ok (UART THR)\n"
