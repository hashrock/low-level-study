# lib.s — 他のファイルから呼ばれる関数群
#   puts(a0 = NUL 終端文字列)  : 文字列を stdout に書く
#   print_u64(a0 = 値)          : 符号なし 10 進数 + 改行を書く
    .text
    .globl puts
puts:
    mv   a1, a0             # a1 = 文字列の先頭
    mv   a2, a0
.Lstrlen:
    lbu  t0, 0(a2)
    beqz t0, .Lstrlen_done
    addi a2, a2, 1
    j    .Lstrlen
.Lstrlen_done:
    sub  a2, a2, a1         # a2 = 長さ
    li   a0, 1
    li   a7, 64             # write
    ecall
    ret

    .globl print_u64
print_u64:
    # バッファの末尾から 1 桁ずつ埋めていく
    la   t1, numbuf + 20    # t1 = 書き込み位置 (末尾)
    li   t2, '\n'
    sb   t2, 0(t1)
    li   t3, 10
.Ldigit:
    addi t1, t1, -1
    remu t0, a0, t3         # t0 = a0 % 10
    addi t0, t0, '0'
    sb   t0, 0(t1)
    divu a0, a0, t3
    bnez a0, .Ldigit
    # write(1, t1, numbuf+21 - t1)
    la   a2, numbuf + 21
    sub  a2, a2, t1
    mv   a1, t1
    li   a0, 1
    li   a7, 64
    ecall
    ret

    .bss
numbuf:
    .zero 24
