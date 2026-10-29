//! ユーザプログラムの一覧。ELF ローダはないので、プログラムは
//! カーネルイメージに一緒にリンクされた関数で、spawn で名前から探す。
const std = @import("std");
const u = @import("ulib.zig");
pub const shell_main = @import("shell.zig").main;

pub const Entry = struct { name: []const u8, func: *const fn () callconv(.c) noreturn };

pub const table = [_]Entry{
    .{ .name = "sh", .func = &shell_main },
    .{ .name = "counter", .func = &counterMain },
    .{ .name = "spin", .func = &spinMain },
};

pub fn find(name: []const u8) ?Entry {
    for (table) |e| if (std.mem.eql(u8, e.name, name)) return e;
    return null;
}

/// 1 秒ごとに数を数えて 5 回で終わる (sleep システムコールの例)
fn counterMain() callconv(.c) noreturn {
    const pid = u.getpid();
    var i: u32 = 1;
    while (i <= 5) : (i += 1) {
        u.sleep(100);
        u.print(1, "\n[counter pid={d}] {d}\n", .{ pid, i });
    }
    u.exit();
}

/// CPU を占有し続けるループ。それでもシェルが動くのはタイマでプリエンプトされるから
fn spinMain() callconv(.c) noreturn {
    const start = u.uptime();
    var x: u64 = 0;
    while (u.uptime() - start < 300) { // 約 3 秒ビジーループ
        var i: u32 = 0;
        while (i < 100_000) : (i += 1) std.mem.doNotOptimizeAway(&x);
        x += 1;
    }
    u.print(1, "\n[spin pid={d}] done, looped {d} times\n", .{ u.getpid(), x });
    u.exit();
}
