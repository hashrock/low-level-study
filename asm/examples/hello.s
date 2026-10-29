# hello.s — write(1, msg, len) して exit(0) するだけの最小プログラム
    .equ SYS_WRITE, 64
    .equ SYS_EXIT, 93

    .data
msg:
    .ascii "Hello, RISC-V!\n"
    .equ MSG_LEN, . - msg   # "." は現在位置。li で使うので使用箇所より前に定義する

    .text
    .globl _start
_start:
    li   a0, 1              # fd = stdout
    la   a1, msg            # buf (auipc + addi に展開される)
    li   a2, MSG_LEN        # len
    li   a7, SYS_WRITE
    ecall

    li   a0, 0              # status
    li   a7, SYS_EXIT
    ecall
