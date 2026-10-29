//! ユーザ空間ライブラリ: ecall でカーネルを呼ぶ薄いラッパ (libc のようなもの)
const std = @import("std");
pub const sys = @import("../syscall_nums.zig");

fn syscall(num: u64, a0: u64, a1: u64, a2: u64, a3: u64) u64 {
    return asm volatile ("ecall"
        : [ret] "={x10}" (-> u64),
        : [num] "{x17}" (num),
          [a0] "{x10}" (a0),
          [a1] "{x11}" (a1),
          [a2] "{x12}" (a2),
          [a3] "{x13}" (a3),
        : .{ .memory = true });
}

const ERR: u64 = @bitCast(@as(i64, -1));
pub const Error = error{Failed};

fn check(r: u64) Error!u64 {
    return if (r == ERR) error.Failed else r;
}

fn ptr(s: []const u8) u64 {
    return @intFromPtr(s.ptr);
}

pub fn exit() noreturn {
    _ = syscall(sys.EXIT, 0, 0, 0, 0);
    unreachable;
}
pub fn read(fd: u64, buf: []u8) Error!usize {
    return check(syscall(sys.READ, fd, ptr(buf), buf.len, 0));
}
pub fn write(fd: u64, buf: []const u8) void {
    _ = syscall(sys.WRITE, fd, ptr(buf), buf.len, 0);
}
pub fn open(path: []const u8, flags: u64) Error!u64 {
    return check(syscall(sys.OPEN, ptr(path), path.len, flags, 0));
}
pub fn close(fd: u64) void {
    _ = syscall(sys.CLOSE, fd, 0, 0, 0);
}
pub fn mkdir(path: []const u8) Error!void {
    _ = try check(syscall(sys.MKDIR, ptr(path), path.len, 0, 0));
}
pub fn unlink(path: []const u8) Error!void {
    _ = try check(syscall(sys.UNLINK, ptr(path), path.len, 0, 0));
}
pub fn chdir(path: []const u8) Error!void {
    _ = try check(syscall(sys.CHDIR, ptr(path), path.len, 0, 0));
}
pub fn getcwd(buf: []u8) []u8 {
    return buf[0..syscall(sys.GETCWD, ptr(buf), buf.len, 0, 0)];
}
pub const DirEntry = struct { name: []const u8, is_dir: bool, size: usize };
pub fn readdir(path: []const u8, index: usize, namebuf: []u8) ?DirEntry {
    const r = syscall(sys.READDIR, ptr(path), path.len, index, ptr(namebuf));
    if (r == ERR) return null;
    return .{ .name = namebuf[0 .. r & 0xffff], .is_dir = (r >> 16) & 1 != 0, .size = r >> 20 };
}
pub fn ps(buf: []u8) []u8 {
    return buf[0..syscall(sys.PS, ptr(buf), buf.len, 0, 0)];
}
pub fn uptime() u64 {
    return syscall(sys.UPTIME, 0, 0, 0, 0);
}
pub fn sleep(ticks: u64) void {
    _ = syscall(sys.SLEEP, ticks, 0, 0, 0);
}
pub fn spawn(name: []const u8) Error!u64 {
    return check(syscall(sys.SPAWN, ptr(name), name.len, 0, 0));
}
pub fn getpid() u64 {
    return syscall(sys.GETPID, 0, 0, 0, 0);
}
pub fn halt(reboot: bool) noreturn {
    _ = syscall(sys.HALT, @intFromBool(reboot), 0, 0, 0);
    unreachable;
}

/// fd へ printf
pub fn print(fd: u64, comptime fmt: []const u8, args: anytype) void {
    var buf: [256]u8 = undefined;
    write(fd, std.fmt.bufPrint(&buf, fmt, args) catch "<overflow>");
}
