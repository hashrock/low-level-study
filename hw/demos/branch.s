# branch.s — web/hw のデモ: 条件分岐 (成立 / 不成立)
    .text
    .globl _start
_start:
    li   t0, 3
    li   t1, 3
    beq  t0, t1, .Lequal        # 3 == 3 → 成立: pc ← pc + imm
    li   a0, 1                  # (飛ばされる)
.Lequal:
    li   t1, 4
    beq  t0, t1, .Lnever        # 3 != 4 → 不成立: pc ← pc + 4
    blt  t0, t1, .Lless         # 3 < 4 (符号付き) → 成立
    li   a0, 2                  # (飛ばされる)
.Lless:
.Lnever:
    li   a0, 0
    li   a7, 93
    ecall
