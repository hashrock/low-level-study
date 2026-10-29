# forward.s — web/hw のデモ: フォワーディング (EX/MEM → EX と MEM/WB → EX)
    .text
    .globl _start
_start:
    li   t0, 10
    addi t1, t0, 1              # t0 は 1 つ前の命令の結果: EX/MEM から (fwd_a = 1)
    addi t2, t0, 2              # t0 は 2 つ前: MEM/WB から (fwd_a = 2)
    add  t3, t1, t2             # t2 は EX/MEM から (fwd_b = 1)、t1 は MEM/WB から (fwd_a = 2)
    nop
    nop
    add  t4, t3, t3             # 3 つ以上前ならレジスタファイルから (同じサイクルの WB 書き込みも ID で拾う)
    mv   a0, t4
    li   a7, 93
    ecall
