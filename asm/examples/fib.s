# fib.s — fib(0..N) を再帰で計算して 1 行ずつ表示する (単体で完結)
    .equ N, 50
    .equ SYS_WRITE, 64
    .equ SYS_EXIT, 93

    .text
    .globl _start
_start:
main:
    li   s0, 0              # i
    li   s1, N
.Lloop:
    bgt  s0, s1, .Lexit
    mv   a0, s0
    call fib_iter
    call print_u64
    addi s0, s0, 1
    j    .Lloop
.Lexit:
    # 最後に再帰版でも fib(20) = 6765 を計算して表示
    li   a0, 20
    call fib_rec
    call print_u64
    li   a0, 0
    li   a7, SYS_EXIT
    ecall

# fib_iter(a0 = n) -> a0 : ループ版
fib_iter:
    li   t0, 0              # a
    li   t1, 1              # b
.Lfi:
    beqz a0, .Lfi_done
    add  t2, t0, t1
    mv   t0, t1
    mv   t1, t2
    addi a0, a0, -1
    j    .Lfi
.Lfi_done:
    mv   a0, t0
    ret

# fib_rec(a0 = n) -> a0 : 再帰版 (スタックフレームの例)
fib_rec:
    li   t0, 2
    blt  a0, t0, .Lfr_base
    addi sp, sp, -32
    sd   ra, 24(sp)
    sd   s0, 16(sp)
    sd   s1, 8(sp)
    mv   s0, a0
    addi a0, a0, -1
    call fib_rec
    mv   s1, a0             # s1 = fib(n-1)
    addi a0, s0, -2
    call fib_rec
    add  a0, a0, s1         # fib(n-1) + fib(n-2)
    ld   s1, 8(sp)
    ld   s0, 16(sp)
    ld   ra, 24(sp)
    addi sp, sp, 32
.Lfr_base:
    ret

# print_u64(a0) : 10 進数 + 改行
print_u64:
    la   t1, buf + 20
    li   t2, '\n'
    sb   t2, 0(t1)
    li   t3, 10
.Ld:
    addi t1, t1, -1
    remu t0, a0, t3
    addi t0, t0, '0'
    sb   t0, 0(t1)
    divu a0, a0, t3
    bnez a0, .Ld
    la   a2, buf + 21
    sub  a2, a2, t1
    mv   a1, t1
    li   a0, 1
    li   a7, SYS_WRITE
    ecall
    ret

    .bss
buf:
    .zero 32
