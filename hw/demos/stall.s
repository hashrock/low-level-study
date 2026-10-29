# stall.s — web/hw のデモ: RAM に待ちサイクル (wait=2) を入れて、バスの ready 待ちでストールさせる
    .text
    .globl _start
_start:
    la   t0, value
    ld   a1, 0(t0)              # RAM が 2 サイクル ready を返さない → 同じ命令が 3 サイクル続く
    addi a1, a1, 1
    sd   a1, 8(t0)              # ストアも同じく待つ
    li   t1, 0x10000000
    li   t2, 'A'
    sb   t2, 0(t1)              # コンソールは待ちなし
    li   a0, 0
    li   a7, 93
    ecall
    .data
    .align 3
value:
    .dword 41
    .dword 0
