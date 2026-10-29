# flush.s — web/hw のデモ: 分岐成立で後ろの 2 命令を捨てる (パイプライン版のフラッシュ)
    .text
    .globl _start
_start:
    li   t0, 1
    li   t1, 1
    beq  t0, t1, .Ltarget       # EX で成立が分かる。それまでに読んだ 2 命令は間違い
    addi t0, t0, 100            # (捨てられる)
    addi t1, t1, 100            # (捨てられる)
    addi t2, t2, 100            # (読まれもしない)
.Ltarget:
    addi a0, t0, -1             # 飛び先。a0 = 0
    li   a7, 93
    ecall
