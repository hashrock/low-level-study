//! zigos カーネルのエントリ。
//! QEMU (-bios none) はリセット後 M-mode で 0x80000000 (= _start) から実行を始める。
const std = @import("std");
const riscv = @import("riscv.zig");
const uart = @import("uart.zig");
const fs = @import("fs.zig");
const proc = @import("proc.zig");
const trap = @import("trap.zig");
const user = @import("user/programs.zig");

// ブート直後: hart 0 以外は眠らせ、スタックを設定し、BSS をゼロクリアして kmain へ。
comptime {
    asm (
        \\.section .text.boot
        \\.globl _start
        \\_start:
        \\  csrr t0, mhartid
        \\  bnez t0, park
        \\  la sp, __stack_top
        \\  la t0, __bss_start
        \\  la t1, __bss_end
        \\1:
        \\  bgeu t0, t1, 2f
        \\  sd zero, 0(t0)
        \\  addi t0, t0, 8
        \\  j 1b
        \\2:
        \\  call kmain
        \\park:
        \\  wfi
        \\  j park
    );
}

export fn kmain() callconv(.c) noreturn {
    uart.init();
    uart.puts("\n[boot] zigos starting on RISC-V 64 (M-mode)\n");

    fs.init();
    uart.puts("[boot] ramfs ready\n");

    // トラップの入口を登録
    riscv.csrWrite("mtvec", @intFromPtr(&trap.trap_vector));

    // PMP: U-mode が全メモリにアクセスできるよう許可 (これをしないと U-mode は何も触れない)
    riscv.csrWrite("pmpaddr0", ~@as(u64, 0) >> 10);
    riscv.csrWrite("pmpcfg0", 0x1f); // NAPOT, R/W/X

    // タイマ割り込み有効化
    riscv.setTimer(riscv.TIMER_INTERVAL);
    riscv.csrSet("mie", riscv.MIE_MTIE);
    uart.print("[boot] timer: {d} Hz tick\n", .{riscv.TICK_HZ});

    // mret したとき U-mode (MPP=0) に落ち、割り込みが有効 (MPIE=1) になるよう設定
    var ms = riscv.csrRead("mstatus");
    ms &= ~riscv.MSTATUS_MPP_MASK;
    ms |= riscv.MSTATUS_MPIE;
    riscv.csrWrite("mstatus", ms);

    _ = proc.spawn("sh", &user.shell_main, fs.ROOT);
    uart.puts("[boot] starting init process (sh) in U-mode\n\n");
    proc.current = 0;
    trap.return_to_user(&proc.procs[0].frame);
}

fn kpanic(msg: []const u8, _: ?usize) noreturn {
    uart.print("\n!!! KERNEL PANIC: {s}\n", .{msg});
    while (true) riscv.wfi();
}

pub const panic = std.debug.FullPanic(kpanic);
