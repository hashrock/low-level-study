# demo.s — 波形で見るための短いプログラム (README の「波形を読む」で使う)
#   make wave PROG=examples/demo.s            待ちなし
#   make wave PROG=examples/demo.s WAIT=2     RAM が 2 サイクル遅い (ストールが見える)
    .text
    .globl _start
_start:
    li   a0, 5
    addi a0, a0, -1             # 0xfff50513: a0 = 5 + (-1) = 4
    la   t0, value
    ld   a1, 0(t0)              # RAM からのロード
    li   t1, 0x10000000         # MMIO コンソール (UART THR)
    li   t2, 'A'
    sb   t2, 0(t1)              # コンソールへのストア → 'A' が出る
    li   t2, '\n'
    sb   t2, 0(t1)
    li   a0, 0
    li   a7, 93                 # exit(0)
    ecall
    .data
    .align 3
value:
    .dword 0x1122334455667788
