//! トラップ (割り込み・例外) の入口と、システムコールの実装
const std = @import("std");
const riscv = @import("riscv.zig");
const uart = @import("uart.zig");
const proc = @import("proc.zig");
const fs = @import("fs.zig");
const sys = @import("syscall_nums.zig");
const user = @import("user/programs.zig");

// トラップベクタ。mtvec にこのアドレスを入れておくと、U-mode で ecall や
// タイマ割り込みが起きたとき CPU は M-mode に切り替わってここへ飛んでくる。
//   mscratch : 現在のプロセスの TrapFrame へのポインタ
// 1. 全レジスタを TrapFrame に退避
// 2. カーネルスタックに切り替えて trap_handler(frame) を呼ぶ
// 3. 戻り値の TrapFrame (別プロセスかもしれない!) からレジスタを復元して mret
// 「戻る先の TrapFrame を差し替える」ことがそのままコンテキストスイッチになる。
comptime {
    asm (
        \\.section .text
        \\.globl trap_vector
        \\.align 4
        \\trap_vector:
        \\  csrrw t6, mscratch, t6
        \\  sd x1, 8(t6)
        \\  sd x2, 16(t6)
        \\  sd x3, 24(t6)
        \\  sd x4, 32(t6)
        \\  sd x5, 40(t6)
        \\  sd x6, 48(t6)
        \\  sd x7, 56(t6)
        \\  sd x8, 64(t6)
        \\  sd x9, 72(t6)
        \\  sd x10, 80(t6)
        \\  sd x11, 88(t6)
        \\  sd x12, 96(t6)
        \\  sd x13, 104(t6)
        \\  sd x14, 112(t6)
        \\  sd x15, 120(t6)
        \\  sd x16, 128(t6)
        \\  sd x17, 136(t6)
        \\  sd x18, 144(t6)
        \\  sd x19, 152(t6)
        \\  sd x20, 160(t6)
        \\  sd x21, 168(t6)
        \\  sd x22, 176(t6)
        \\  sd x23, 184(t6)
        \\  sd x24, 192(t6)
        \\  sd x25, 200(t6)
        \\  sd x26, 208(t6)
        \\  sd x27, 216(t6)
        \\  sd x28, 224(t6)
        \\  sd x29, 232(t6)
        \\  sd x30, 240(t6)
        \\  csrr t5, mscratch
        \\  sd t5, 248(t6)
        \\  csrr t5, mepc
        \\  sd t5, 256(t6)
        \\  la sp, __stack_top
        \\  mv a0, t6
        \\  call trap_handler
        \\
        \\.globl return_to_user
        \\return_to_user:
        \\  ld t5, 256(a0)
        \\  csrw mepc, t5
        \\  csrw mscratch, a0
        \\  mv t6, a0
        \\  ld x1, 8(t6)
        \\  ld x2, 16(t6)
        \\  ld x3, 24(t6)
        \\  ld x4, 32(t6)
        \\  ld x5, 40(t6)
        \\  ld x6, 48(t6)
        \\  ld x7, 56(t6)
        \\  ld x8, 64(t6)
        \\  ld x9, 72(t6)
        \\  ld x10, 80(t6)
        \\  ld x11, 88(t6)
        \\  ld x12, 96(t6)
        \\  ld x13, 104(t6)
        \\  ld x14, 112(t6)
        \\  ld x15, 120(t6)
        \\  ld x16, 128(t6)
        \\  ld x17, 136(t6)
        \\  ld x18, 144(t6)
        \\  ld x19, 152(t6)
        \\  ld x20, 160(t6)
        \\  ld x21, 168(t6)
        \\  ld x22, 176(t6)
        \\  ld x23, 184(t6)
        \\  ld x24, 192(t6)
        \\  ld x25, 200(t6)
        \\  ld x26, 208(t6)
        \\  ld x27, 216(t6)
        \\  ld x28, 224(t6)
        \\  ld x29, 232(t6)
        \\  ld x30, 240(t6)
        \\  ld x31, 248(t6)
        \\  mret
    );
}

pub extern fn trap_vector() void;
pub extern fn return_to_user(frame: *proc.TrapFrame) noreturn;

const INTERRUPT_BIT: u64 = 1 << 63;
const IRQ_M_TIMER = 7;
const EXC_ECALL_U = 8;

export fn trap_handler(frame: *proc.TrapFrame) callconv(.c) *proc.TrapFrame {
    const cause = riscv.csrRead("mcause");
    if (cause & INTERRUPT_BIT != 0) {
        if (cause & 0xff == IRQ_M_TIMER) {
            proc.tick();
            return proc.schedule(); // プリエンプション: 時間切れなので次のプロセスへ
        }
        uart.print("unexpected interrupt {d}\n", .{cause & 0xff});
        return frame;
    }
    if (cause == EXC_ECALL_U) {
        return syscall(frame);
    }
    // それ以外の例外 (不正命令, アクセス違反など) はプロセスを殺す
    uart.print("\n[kernel] pid {d} ({s}): exception mcause={d} mepc=0x{x} mtval=0x{x} -> killed\n", .{
        proc.cur().pid, proc.cur().name, cause, frame.pc, riscv.csrRead("mtval"),
    });
    if (riscv.csrRead("mstatus") & riscv.MSTATUS_MPP_MASK != 0) @panic("exception in kernel");
    proc.exit();
    return proc.schedule();
}

fn userSlice(ptr: u64, len: u64) []u8 {
    // ページングがないのでユーザのポインタをそのまま使える (保護もない!)
    return @as([*]u8, @ptrFromInt(ptr))[0..len];
}

const ERR: u64 = @bitCast(@as(i64, -1));

fn syscall(f: *proc.TrapFrame) *proc.TrapFrame {
    const p = proc.cur();
    const num = f.regs[17]; // a7 = システムコール番号
    const a0 = f.arg(0);
    const a1 = f.arg(1);
    const a2 = f.arg(2);
    const a3 = f.arg(3);
    f.pc += 4; // ecall の次の命令から再開する

    const ret: u64 = switch (num) {
        sys.EXIT => {
            proc.exit();
            return proc.schedule();
        },
        sys.YIELD => {
            f.regs[10] = 0;
            return proc.schedule();
        },
        sys.READ => blk: {
            const buf = userSlice(a1, a2);
            if (a0 < 3) {
                // コンソール入力。無ければ待ち状態にして ecall をやり直させる
                if (uart.getc()) |c| {
                    buf[0] = c;
                    break :blk 1;
                }
                f.pc -= 4;
                p.state = .wait_input;
                return proc.schedule();
            }
            const file = getFile(p, a0) orelse break :blk ERR;
            const node = &fs.nodes[file.ino];
            const n = @min(buf.len, node.size -| file.off);
            @memcpy(buf[0..n], node.data[file.off .. file.off + n]);
            file.off += n;
            break :blk n;
        },
        sys.WRITE => blk: {
            const buf = userSlice(a1, a2);
            if (a0 < 3) {
                uart.puts(buf);
                break :blk buf.len;
            }
            const file = getFile(p, a0) orelse break :blk ERR;
            const node = &fs.nodes[file.ino];
            const n = @min(buf.len, fs.MAX_FILE - file.off);
            @memcpy(node.data[file.off .. file.off + n], buf[0..n]);
            file.off += n;
            node.size = @max(node.size, file.off);
            break :blk n;
        },
        sys.OPEN => sysOpen(p, userSlice(a0, a1), a2),
        sys.CLOSE => blk: {
            const file = getFile(p, a0) orelse break :blk ERR;
            file.used = false;
            break :blk 0;
        },
        sys.MKDIR => if (fs.create(p.cwd, userSlice(a0, a1), true)) |_| 0 else |_| ERR,
        sys.UNLINK => if (fs.remove(p.cwd, userSlice(a0, a1))) |_| 0 else |_| ERR,
        sys.CHDIR => blk: {
            const ino = fs.lookup(p.cwd, userSlice(a0, a1)) catch break :blk ERR;
            if (!fs.nodes[ino].is_dir) break :blk ERR;
            p.cwd = ino;
            break :blk 0;
        },
        sys.GETCWD => blk: {
            var tmp: [256]u8 = undefined;
            const path = fs.absPath(p.cwd, &tmp);
            const out = userSlice(a0, a1);
            const n = @min(out.len, path.len);
            @memcpy(out[0..n], path[0..n]);
            break :blk n;
        },
        sys.READDIR => blk: {
            // readdir(path, len, index, namebuf[28]) -> 名前の長さ | (dir なら 1<<16)
            const dir = fs.lookup(p.cwd, userSlice(a0, a1)) catch break :blk ERR;
            if (!fs.nodes[dir].is_dir) break :blk ERR;
            const ino = fs.nthChild(dir, a2) orelse break :blk ERR;
            const node = &fs.nodes[ino];
            const name = node.getName();
            @memcpy(userSlice(a3, name.len), name);
            break :blk name.len | (@as(u64, @intFromBool(node.is_dir)) << 16) | (@as(u64, node.size) << 20);
        },
        sys.PS => proc.psText(userSlice(a0, a1)),
        sys.UPTIME => proc.ticks,
        sys.SLEEP => {
            p.state = .sleeping;
            p.wake_tick = proc.ticks + a0;
            f.regs[10] = 0;
            return proc.schedule();
        },
        sys.SPAWN => blk: {
            const entry = user.find(userSlice(a0, a1)) orelse break :blk ERR;
            break :blk proc.spawn(entry.name, entry.func, p.cwd) orelse ERR;
        },
        sys.GETPID => p.pid,
        sys.HALT => {
            // QEMU virt の "sifive_test" デバイスに書くと電源断/リセットできる
            const test_dev: *volatile u32 = @ptrFromInt(0x10_0000);
            test_dev.* = if (a0 == 1) 0x7777 else 0x5555;
            while (true) riscv.wfi();
        },
        else => ERR,
    };
    f.regs[10] = ret; // 戻り値は a0
    return f;
}

fn getFile(p: *proc.Proc, fd: u64) ?*proc.File {
    if (fd < 3 or fd - 3 >= proc.NFILE) return null;
    const file = &p.files[fd - 3];
    return if (file.used) file else null;
}

fn sysOpen(p: *proc.Proc, path: []const u8, flags: u64) u64 {
    const ino = fs.lookup(p.cwd, path) catch |e| blk: {
        if (e == error.NotFound and flags & sys.O_CREAT != 0)
            break :blk fs.create(p.cwd, path, false) catch return ERR;
        return ERR;
    };
    const node = &fs.nodes[ino];
    if (node.is_dir) return ERR;
    if (flags & sys.O_TRUNC != 0) node.size = 0;
    for (&p.files, 0..) |*file, i| {
        if (file.used) continue;
        file.* = .{ .used = true, .ino = ino, .off = if (flags & sys.O_APPEND != 0) node.size else 0 };
        return i + 3;
    }
    return ERR;
}
