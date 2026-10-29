# loaduse.s — web/hw のデモ: load-use ハザード (パイプライン版で 1 サイクルのバブル)
    .text
    .globl _start
_start:
    la   s0, value
    ld   t1, 0(s0)              # ロード
    addi t2, t1, 1              # ← 直後に t1 を使う: 値は MEM が終わるまで無い → 1 サイクル止まる
    ld   t3, 8(s0)              # ロード
    addi t4, s0, 0              # 間に無関係な命令が 1 つあれば…
    add  t5, t3, t2             # MEM/WB からフォワードできるので止まらない
    li   a0, 0
    li   a7, 93
    ecall
    .data
    .align 3
value:
    .dword 41
    .dword 100
