# loadstore.s — ロード / ストアの全サイズ、符号拡張と 0 拡張、ワード内のバイト位置
    .text
    .globl _start
_start:
    la s0, buf

    li t6, 1                    # sd / ld
    li t0, 0x8877665544332211
    sd t0, 0(s0)
    ld a0, 0(s0)
    bne a0, t0, fail

    li t6, 2                    # リトルエンディアン: 番地の小さいバイトが下位
    lbu a0, 0(s0)
    li a1, 0x11
    bne a0, a1, fail
    lbu a0, 7(s0)
    li a1, 0x88
    bne a0, a1, fail
    lb a0, 7(s0)                # 0x88 は負 → 符号拡張
    li a1, -120
    bne a0, a1, fail

    li t6, 3                    # half
    lhu a0, 6(s0)
    li a1, 0x8877
    bne a0, a1, fail
    lh a0, 6(s0)
    li a1, 0xffffffffffff8877
    bne a0, a1, fail
    lh a0, 2(s0)
    li a1, 0x4433
    bne a0, a1, fail

    li t6, 4                    # word
    lwu a0, 4(s0)
    li a1, 0x88776655
    bne a0, a1, fail
    lw a0, 4(s0)
    li a1, 0xffffffff88776655
    bne a0, a1, fail
    lw a0, 0(s0)
    li a1, 0x44332211
    bne a0, a1, fail

    li t6, 5                    # sb / sh / sw は wstrb で一部のバイトだけ書き換える
    li t0, 0xaa
    sb t0, 1(s0)
    li t0, 0xccbb
    sh t0, 2(s0)
    li t0, 0x12345678ffeeddcc   # sw は下位 32 ビットだけ
    sw t0, 4(s0)
    ld a0, 0(s0)
    li a1, 0xffeeddccccbbaa11
    bne a0, a1, fail

    li t6, 6                    # 負のオフセット, 2 つ目のワード
    addi s1, s0, 16
    li t0, -2
    sd t0, -8(s1)
    ld a0, 8(s0)
    bne a0, t0, fail
    lbu a0, -1(s1)
    li a1, 0xff
    bne a0, a1, fail

    la a1, msg
    li a2, 13
    li a0, 1
    li a7, 64
    ecall
    li a0, 0
    li a7, 93
    ecall
fail:
    mv a0, t6
    li a7, 93
    ecall
    .data
msg: .ascii "loadstore ok\n"
    .bss
    .align 3
buf: .zero 32
