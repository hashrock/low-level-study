//! プロセス管理とラウンドロビンスケジューラ
//! ページングなし: 全プロセスはカーネルと同じアドレス空間で動き、
//! 違いは「U-mode で動く」「自分のスタックとレジスタ退避領域(TrapFrame)を持つ」ことだけ。
const std = @import("std");
const riscv = @import("riscv.zig");
const uart = @import("uart.zig");
const fs = @import("fs.zig");

pub const NPROC = 8;
pub const NFILE = 8;
const STACK_SIZE = 16 * 1024;

/// トラップ時に全レジスタを退避する領域。オフセットは trap.zig のアセンブリと一致させること。
pub const TrapFrame = extern struct {
    regs: [32]u64, // regs[i] = x_i (x0 は未使用)
    pc: u64, // mepc

    pub fn arg(self: *TrapFrame, n: usize) u64 {
        return self.regs[10 + n]; // a0..a7 = x10..x17
    }
};

pub const State = enum { unused, runnable, sleeping, wait_input };

pub const File = struct { used: bool = false, ino: u16 = 0, off: usize = 0 };

pub const Proc = struct {
    state: State = .unused,
    pid: u32 = 0,
    name: []const u8 = "",
    frame: TrapFrame = undefined,
    wake_tick: u64 = 0,
    cwd: u16 = fs.ROOT,
    files: [NFILE]File = @splat(.{}),
};

pub var procs: [NPROC]Proc = @splat(.{});
var stacks: [NPROC][STACK_SIZE]u8 align(16) = undefined;
pub var current: usize = 0;
var next_pid: u32 = 1;
pub var ticks: u64 = 0;

pub fn cur() *Proc {
    return &procs[current];
}

/// entry から U-mode で走るプロセスを作る
pub fn spawn(name: []const u8, entry: *const fn () callconv(.c) noreturn, cwd: u16) ?u32 {
    for (&procs, 0..) |*p, i| {
        if (p.state != .unused) continue;
        p.* = .{ .state = .runnable, .pid = next_pid, .name = name, .cwd = cwd };
        p.frame = std.mem.zeroes(TrapFrame);
        p.frame.pc = @intFromPtr(entry);
        p.frame.regs[2] = @intFromPtr(&stacks[i]) + STACK_SIZE; // sp
        p.frame.regs[3] = riscv.readGp(); // gp はカーネルと共有
        next_pid += 1;
        return p.pid;
    }
    return null;
}

pub fn exit() void {
    cur().state = .unused;
}

fn canRun(p: *Proc) bool {
    return switch (p.state) {
        .runnable => true,
        .sleeping => ticks >= p.wake_tick,
        .wait_input => uart.hasInput(),
        .unused => false,
    };
}

/// タイマ割り込み 1 回分の処理 (次の割り込みを予約して tick を進める)
pub fn tick() void {
    ticks += 1;
    riscv.setTimer(riscv.TIMER_INTERVAL);
}

/// 次に走らせるプロセスを current から順に探す (ラウンドロビン)。
/// 誰も走れなければ wfi で割り込みを待つ (アイドル)。
pub fn schedule() *TrapFrame {
    while (true) {
        var i: usize = 1;
        while (i <= NPROC) : (i += 1) {
            const idx = (current + i) % NPROC;
            const p = &procs[idx];
            if (canRun(p)) {
                p.state = .runnable;
                current = idx;
                return &p.frame;
            }
        }
        // 走れるプロセスがいない。M-mode では MIE=0 だが、wfi は mie で許可された
        // 割り込みが pending になれば起きる。タイマなら tick を進める。
        riscv.wfi();
        if (riscv.timerPending()) tick();
    }
}

/// ps コマンド用のテキストを buf に作る
pub fn psText(buf: []u8) usize {
    var w: usize = 0;
    const head = std.fmt.bufPrint(buf[w..], "PID  STATE       NAME\n", .{}) catch return w;
    w += head.len;
    for (&procs, 0..) |*p, i| {
        if (p.state == .unused) continue;
        const mark = if (i == current) "  <- current" else "";
        const s = std.fmt.bufPrint(buf[w..], "{d:<4} {s:<11} {s}{s}\n", .{ p.pid, @tagName(p.state), p.name, mark }) catch break;
        w += s.len;
    }
    return w;
}
