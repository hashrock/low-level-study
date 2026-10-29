# call.s — web/hw のデモ: jal で呼び出し、jalr (ret) で戻る
    .text
    .globl _start
_start:
    li   a0, 20
    jal  ra, double             # ra = pc + 4、pc ← double
    jal  ra, double             # もう 1 回 (a0 = 80)
    li   a7, 93
    ecall                       # exit(80)

double:                         # a0 = a0 * 2
    add  a0, a0, a0
    jalr zero, 0(ra)            # = ret: pc ← ra (bit0 は 0 に)
