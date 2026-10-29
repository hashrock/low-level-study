# loop.s — web/hw のデモ: 1 + 2 + … + 10 を足すループ (両方のコアで比べる)
    .text
    .globl _start
_start:
    li   a0, 0                  # sum
    li   t0, 1                  # i
    li   t1, 10
.Lloop:
    add  a0, a0, t0             # sum += i
    addi t0, t0, 1              # i++
    ble  t0, t1, .Lloop         # i <= 10 なら繰り返す (= bge t1, t0)
    li   a7, 93
    ecall                       # exit(55)
