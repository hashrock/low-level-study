//! U-mode で動くシェル。カーネルの機能はすべて ecall (システムコール) 経由で使う。
const std = @import("std");
const u = @import("ulib.zig");

const STDIN = 0;
const STDOUT = 1;

var out: u64 = STDOUT; // リダイレクト時はファイルの fd になる

fn say(s: []const u8) void {
    u.write(out, s);
}

/// 1 行読む。バックスペースとエコーバックはシェル自身がやる (端末は raw)
fn readLine(buf: []u8) []u8 {
    var len: usize = 0;
    while (true) {
        var c: [1]u8 = undefined;
        _ = u.read(STDIN, &c) catch continue;
        switch (c[0]) {
            '\r', '\n' => {
                u.write(STDOUT, "\n");
                return buf[0..len];
            },
            0x7f, 0x08 => if (len > 0) { // Backspace / DEL
                len -= 1;
                u.write(STDOUT, "\x08 \x08");
            },
            0x15 => while (len > 0) : (len -= 1) u.write(STDOUT, "\x08 \x08"), // Ctrl-U
            else => if (c[0] >= 0x20 and len < buf.len) {
                buf[len] = c[0];
                len += 1;
                u.write(STDOUT, &c);
            },
        }
    }
}

const help_text =
    \\commands:
    \\  help                 this message
    \\  echo TEXT...         print text (use > or >> to write a file)
    \\  ls [DIR]             list directory
    \\  cat FILE...          print files
    \\  touch FILE           create empty file
    \\  mkdir DIR            create directory
    \\  rm PATH              remove file or empty directory
    \\  cd [DIR] / pwd       change / print working directory
    \\  ps                   list processes
    \\  uptime               ticks since boot (100 ticks = 1s)
    \\  run PROG [&]         start a user program (counter, spin)
    \\  clear                clear screen
    \\  halt / reboot        power off / restart QEMU
    \\
;

fn cmdLs(args: []const []const u8) void {
    const path = if (args.len > 1) args[1] else ".";
    var name: [32]u8 = undefined;
    var i: usize = 0;
    while (u.readdir(path, i, &name)) |e| : (i += 1) {
        if (e.is_dir) {
            u.print(out, "  {s}/\n", .{e.name});
        } else {
            u.print(out, "  {s:<20} {d} bytes\n", .{ e.name, e.size });
        }
    }
    if (i == 0 and u.readdir(path, 0, &name) == null) {
        // 空ディレクトリかエラーか区別するために cd で確認する代わりに open を試す
        if (u.open(path, 0)) |fd| {
            u.close(fd);
            u.print(out, "  {s}\n", .{path});
        } else |_| {}
    }
}

fn cmdCat(args: []const []const u8) void {
    for (args[1..]) |path| {
        const fd = u.open(path, 0) catch {
            u.print(STDOUT, "cat: {s}: no such file\n", .{path});
            continue;
        };
        var buf: [128]u8 = undefined;
        while (true) {
            const n = u.read(fd, &buf) catch break;
            if (n == 0) break;
            say(buf[0..n]);
        }
        u.close(fd);
    }
}

fn runProgram(args: []const []const u8) void {
    if (args.len < 2) return say("usage: run PROG [&]\n");
    const pid = u.spawn(args[1]) catch {
        u.print(STDOUT, "run: {s}: no such program\n", .{args[1]});
        return;
    };
    const background = args.len > 2 and std.mem.eql(u8, args[2], "&");
    u.print(STDOUT, "[started pid {d}{s}]\n", .{ pid, if (background) " in background" else "" });
    // フォアグラウンドなら ps に出てこなくなるまで待つ (wait システムコールの簡易版)
    if (!background) {
        var buf: [512]u8 = undefined;
        var pat: [16]u8 = undefined;
        const needle = std.fmt.bufPrint(&pat, "\n{d} ", .{pid}) catch return;
        while (std.mem.indexOf(u8, u.ps(&buf), needle) != null) u.sleep(5);
    }
}

fn execute(args: []const []const u8) void {
    const cmd = args[0];
    const eq = struct {
        fn f(a: []const u8, b: []const u8) bool {
            return std.mem.eql(u8, a, b);
        }
    }.f;
    if (eq(cmd, "help")) {
        say(help_text);
    } else if (eq(cmd, "echo")) {
        for (args[1..], 0..) |a, i| {
            if (i > 0) say(" ");
            say(a);
        }
        say("\n");
    } else if (eq(cmd, "ls")) {
        cmdLs(args);
    } else if (eq(cmd, "cat")) {
        cmdCat(args);
    } else if (eq(cmd, "touch")) {
        for (args[1..]) |a| {
            const fd = u.open(a, u.sys.O_CREAT) catch {
                u.print(STDOUT, "touch: {s}: failed\n", .{a});
                continue;
            };
            u.close(fd);
        }
    } else if (eq(cmd, "mkdir")) {
        for (args[1..]) |a| u.mkdir(a) catch u.print(STDOUT, "mkdir: {s}: failed\n", .{a});
    } else if (eq(cmd, "rm")) {
        for (args[1..]) |a| u.unlink(a) catch u.print(STDOUT, "rm: {s}: failed\n", .{a});
    } else if (eq(cmd, "cd")) {
        const dir = if (args.len > 1) args[1] else "/";
        u.chdir(dir) catch u.print(STDOUT, "cd: {s}: not a directory\n", .{dir});
    } else if (eq(cmd, "pwd")) {
        var buf: [256]u8 = undefined;
        say(u.getcwd(&buf));
        say("\n");
    } else if (eq(cmd, "ps")) {
        var buf: [512]u8 = undefined;
        say(u.ps(&buf));
    } else if (eq(cmd, "uptime")) {
        const t = u.uptime();
        u.print(out, "up {d}.{d:0>2}s ({d} ticks)\n", .{ t / 100, t % 100, t });
    } else if (eq(cmd, "run")) {
        runProgram(args);
    } else if (eq(cmd, "clear")) {
        say("\x1b[2J\x1b[H");
    } else if (eq(cmd, "halt")) {
        say("bye.\n");
        u.halt(false);
    } else if (eq(cmd, "reboot")) {
        u.halt(true);
    } else {
        u.print(STDOUT, "{s}: command not found\n", .{cmd});
    }
}

pub fn main() callconv(.c) noreturn {
    // /etc/motd を表示
    execute(&.{ "cat", "/etc/motd" });
    var line_buf: [128]u8 = undefined;
    var cwd_buf: [256]u8 = undefined;
    while (true) {
        u.print(STDOUT, "zigos:{s}$ ", .{u.getcwd(&cwd_buf)});
        const line = readLine(&line_buf);

        // 空白で分割。"> file" / ">> file" はリダイレクトとして扱う
        var args: [16][]const u8 = undefined;
        var argc: usize = 0;
        var redirect: ?[]const u8 = null;
        var append = false;
        var it = std.mem.tokenizeScalar(u8, line, ' ');
        while (it.next()) |tok| {
            if (std.mem.eql(u8, tok, ">") or std.mem.eql(u8, tok, ">>")) {
                append = tok.len == 2;
                redirect = it.next() orelse "";
            } else if (argc < args.len) {
                args[argc] = tok;
                argc += 1;
            }
        }
        if (argc == 0) continue;

        out = STDOUT;
        if (redirect) |path| {
            const flags: u64 = u.sys.O_CREAT | @as(u64, if (append) u.sys.O_APPEND else u.sys.O_TRUNC);
            out = u.open(path, flags) catch {
                u.print(STDOUT, "sh: cannot open {s}\n", .{path});
                continue;
            };
        }
        execute(args[0..argc]);
        if (out != STDOUT) u.close(out);
    }
}
