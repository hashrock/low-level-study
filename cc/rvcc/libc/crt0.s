# crt0.s — プログラムの入口とシステムコールの薄いラッパー (RISC-V アセンブリ)
# libc の残りは C で書いてあり (libc.c)、rvcc 自身でコンパイルされる。
#
# Linux (と ../emu) はプロセス開始時のスタックをこう用意する:
#   sp -> argc
#         argv[0] ... argv[argc-1], 0
#         envp ..., 0
#         auxv ...

	.text
	.globl _start
_start:
	ld a0, 0(sp)            # argc
	addi a1, sp, 8          # argv
	call main               # main(argc, argv)
	call exit               # exit(main の戻り値)

# システムコール: 番号を a7、引数を a0.. に入れて ecall。結果は a0。
# C の関数と同じ規約で呼べるので、引数はそのまま a0.. に届いている。
	.globl exit
exit:
	li a7, 93
	ecall

	.globl write
write:
	li a7, 64
	ecall
	ret

	.globl read
read:
	li a7, 63
	ecall
	ret

	.globl __brk
__brk:
	li a7, 214
	ecall
	ret
