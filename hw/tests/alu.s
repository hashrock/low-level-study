# alu.s — RV64I の整数演算 (レジスタ-レジスタ / 即値 / lui / auipc) のテスト
# 各ケース: t6 = ケース番号, a0 = 計算結果, a1 = 期待値。違えば exit(ケース番号)
    .text
    .globl _start
_start:
    li t6, 1
    li t0, 5
    li t1, -3
    add a0, t0, t1
    li a1, 2
    bne a0, a1, fail

    li t6, 2
    sub a0, t1, t0              # -3 - 5
    li a1, -8
    bne a0, a1, fail

    li t6, 3                    # 64 ビットの桁あふれは回り込む
    li t0, 0x7fffffffffffffff
    addi a0, t0, 1
    li a1, 0x8000000000000000
    bne a0, a1, fail

    li t6, 4
    li t0, 0xf0f0
    li t1, 0x0ff0
    and a0, t0, t1
    li a1, 0x00f0
    bne a0, a1, fail
    or  a0, t0, t1
    li a1, 0xfff0
    bne a0, a1, fail
    xor a0, t0, t1
    li a1, 0xff00
    bne a0, a1, fail

    li t6, 5
    andi a0, t0, -16            # 即値は符号拡張される (0xff...f0)
    li a1, 0xf0f0
    bne a0, a1, fail
    ori  a0, x0, -1
    li a1, -1
    bne a0, a1, fail
    xori a0, t0, -1             # not
    li a1, ~0xf0f0
    bne a0, a1, fail

    li t6, 6                    # シフト: シフト量は下位 6 ビットだけ
    li t0, 1
    li t1, 65
    sll a0, t0, t1              # 1 << (65 & 63) = 2
    li a1, 2
    bne a0, a1, fail
    slli a0, t0, 63
    li a1, 0x8000000000000000
    bne a0, a1, fail
    srli a0, a0, 63             # 論理シフト: 上は 0
    li a1, 1
    bne a0, a1, fail

    li t6, 7
    li t0, -256
    srai a0, t0, 4              # 算術シフト: 上は符号で埋まる
    li a1, -16
    bne a0, a1, fail
    li t1, 4
    sra a0, t0, t1
    bne a0, a1, fail
    srl a0, t0, t1
    li a1, 0x0ffffffffffffff0
    bne a0, a1, fail

    li t6, 8                    # 比較
    li t0, -1
    li t1, 1
    slt a0, t0, t1              # -1 < 1 (符号付き)
    li a1, 1
    bne a0, a1, fail
    sltu a0, t0, t1             # 0xff..ff < 1 ? (符号なし) → 0
    bne a0, x0, fail
    slti a0, t0, 0
    li a1, 1
    bne a0, a1, fail
    sltiu a0, x0, 1             # seqz
    bne a0, a1, fail
    sltiu a0, t0, -1            # 0xff..ff < 0xff..ff → 0
    bne a0, x0, fail

    li t6, 9                    # lui は上位 20 ビット + 符号拡張
    lui a0, 0x80000
    li a1, 0xffffffff80000000
    bne a0, a1, fail
    lui a0, 0x12345
    li a1, 0x12345000
    bne a0, a1, fail

    li t6, 10                   # auipc = pc + (imm << 12)
.Lhere:
    auipc a0, 0
    la a1, .Lhere
    bne a0, a1, fail
.Lhere2:
    auipc a0, 1                 # この命令の pc + 0x1000
    la a1, .Lhere2
    li t0, 0x1000
    add a1, a1, t0
    bne a0, a1, fail

    li t6, 11                   # x0 には書けない
    addi x0, x0, 5
    bne x0, zero, fail

    j pass

pass:
    la a1, msg
    li a2, 7
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
msg: .ascii "alu ok\n"
