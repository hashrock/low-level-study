# main.s — lib.s の関数を call で呼ぶ複数ファイルの例
#   go run . build examples/main.s examples/lib.s -o main
    .section .rodata
greeting:
    .asciz "main.s calls puts() in lib.s\n"

    .data
    .align 3
values:
    .dword 7, 6, 1000000007
    .dword 0
table_ptr:
    .dword values           # R_RISCV_64: リンク時に絶対アドレスが入る

    .text
    .globl _start
_start:
    la   a0, greeting
    call puts               # 別ファイルのシンボル → R_RISCV_CALL_PLT

    # values の各要素を 0 が来るまで表示する
    la   t0, table_ptr
    ld   s1, 0(t0)          # s1 = &values (絶対アドレス経由)
.Lloop:
    ld   a0, 0(s1)
    beqz a0, .Ldone
    call print_u64
    addi s1, s1, 8
    j    .Lloop
.Ldone:
    li   a0, 0
    li   a7, 93             # exit
    ecall
