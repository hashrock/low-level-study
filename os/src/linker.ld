/* QEMU virt: RAM は 0x80000000 から。-bios none なのでリセット後ここへ直接ジャンプする */
OUTPUT_ARCH(riscv)
ENTRY(_start)

SECTIONS
{
    . = 0x80000000;
    .text : {
        KEEP(*(.text.boot))   /* _start を必ず先頭に */
        *(.text .text.*)
    }
    .rodata : ALIGN(16) { *(.srodata .srodata.* .rodata .rodata.*) }
    .data   : ALIGN(16) { *(.sdata .sdata.* .data .data.*) }
    .bss    : ALIGN(16) {
        __bss_start = .;
        *(.sbss .sbss.* .bss .bss.*)
        __bss_end = .;
    }
    . = ALIGN(16);
    . += 0x4000;              /* カーネルスタック 16KiB */
    __stack_top = .;
    __kernel_end = .;
}
