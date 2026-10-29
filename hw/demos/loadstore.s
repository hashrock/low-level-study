# loadstore.s — web/hw のデモ: ストアとロード (sw / lw / ld / lb)
    .text
    .globl _start
_start:
    la   t0, buf                # auipc + addi (a_sel = PC)
    li   t1, -2
    sw   t1, 0(t0)              # 4 バイト書く: wstrb = 0000_1111
    lw   a1, 0(t0)              # 符号拡張して a1 = -2
    li   t2, 0x41
    sb   t2, 9(t0)              # 1 バイト書く: アドレスの下位 3 ビット = 1 → wstrb = 0000_0010
    ld   a2, 8(t0)              # 8 バイト読む: 0x1122334455664188
    lbu  a3, 1(t0)              # 0 拡張: a3 = 0xff
    li   a0, 0
    li   a7, 93
    ecall
    .data
    .align 3
buf:
    .dword 0
    .dword 0x1122334455667788
