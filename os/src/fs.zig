//! メモリ上のファイルシステム (ramfs)
//! inode は固定長配列。ディレクトリは「親 inode 番号」を各 inode が持つことで木を表す。
//! ディレクトリの子一覧は全 inode を走査して parent が一致するものを集めるだけ。
const std = @import("std");

pub const MAX_NODES = 64;
pub const MAX_NAME = 28;
pub const MAX_FILE = 2048;
pub const ROOT: u16 = 0;

pub const Node = struct {
    used: bool = false,
    is_dir: bool = false,
    parent: u16 = 0,
    name_len: u8 = 0,
    name: [MAX_NAME]u8 = undefined,
    size: usize = 0,
    data: [MAX_FILE]u8 = undefined,

    pub fn getName(self: *const Node) []const u8 {
        return self.name[0..self.name_len];
    }
};

pub var nodes: [MAX_NODES]Node = undefined;

pub const Error = error{ NotFound, Exists, NotDir, IsDir, NoSpace, NotEmpty, BadName };

fn alloc(parent: u16, name: []const u8, is_dir: bool) Error!u16 {
    if (name.len == 0 or name.len > MAX_NAME) return error.BadName;
    if (std.mem.eql(u8, name, ".") or std.mem.eql(u8, name, "..")) return error.BadName;
    if (child(parent, name) != null) return error.Exists;
    for (&nodes, 0..) |*n, i| {
        if (n.used) continue;
        n.* = .{ .used = true, .is_dir = is_dir, .parent = parent, .name_len = @intCast(name.len) };
        @memcpy(n.name[0..name.len], name);
        return @intCast(i);
    }
    return error.NoSpace;
}

/// ディレクトリ dir の中から name という子を探す
pub fn child(dir: u16, name: []const u8) ?u16 {
    for (&nodes, 0..) |*n, i| {
        if (i == ROOT or !n.used or n.parent != dir) continue;
        if (std.mem.eql(u8, n.getName(), name)) return @intCast(i);
    }
    return null;
}

/// dir の index 番目の子 (readdir 用)
pub fn nthChild(dir: u16, index: usize) ?u16 {
    var k: usize = 0;
    for (&nodes, 0..) |*n, i| {
        if (i == ROOT or !n.used or n.parent != dir) continue;
        if (k == index) return @intCast(i);
        k += 1;
    }
    return null;
}

/// パスを解決して inode 番号を返す。相対パスは cwd 起点。
pub fn lookup(cwd: u16, path: []const u8) Error!u16 {
    var cur: u16 = if (path.len > 0 and path[0] == '/') ROOT else cwd;
    var it = std.mem.tokenizeScalar(u8, path, '/');
    while (it.next()) |comp| {
        if (!nodes[cur].is_dir) return error.NotDir;
        if (std.mem.eql(u8, comp, ".")) continue;
        if (std.mem.eql(u8, comp, "..")) {
            cur = nodes[cur].parent;
            continue;
        }
        cur = child(cur, comp) orelse return error.NotFound;
    }
    return cur;
}

/// "a/b/c" を (a/b の inode, "c") に分ける
fn splitParent(cwd: u16, path: []const u8) Error!struct { dir: u16, name: []const u8 } {
    const trimmed = std.mem.trimEnd(u8, path, "/");
    if (std.mem.lastIndexOfScalar(u8, trimmed, '/')) |i| {
        const dir_path = if (i == 0) "/" else trimmed[0..i];
        const dir = try lookup(cwd, dir_path);
        if (!nodes[dir].is_dir) return error.NotDir;
        return .{ .dir = dir, .name = trimmed[i + 1 ..] };
    }
    if (!nodes[cwd].is_dir) return error.NotDir;
    return .{ .dir = cwd, .name = trimmed };
}

pub fn create(cwd: u16, path: []const u8, is_dir: bool) Error!u16 {
    const p = try splitParent(cwd, path);
    return alloc(p.dir, p.name, is_dir);
}

pub fn remove(cwd: u16, path: []const u8) Error!void {
    const ino = try lookup(cwd, path);
    if (ino == ROOT) return error.BadName;
    if (nodes[ino].is_dir and nthChild(ino, 0) != null) return error.NotEmpty;
    nodes[ino].used = false;
}

/// inode の絶対パスを buf に書く
pub fn absPath(ino: u16, buf: []u8) []const u8 {
    if (ino == ROOT) {
        buf[0] = '/';
        return buf[0..1];
    }
    // 葉から根へたどりながら後ろから詰める
    var end = buf.len;
    var cur = ino;
    while (cur != ROOT) {
        const name = nodes[cur].getName();
        if (end < name.len + 1) break;
        end -= name.len;
        @memcpy(buf[end .. end + name.len], name);
        end -= 1;
        buf[end] = '/';
        cur = nodes[cur].parent;
    }
    return buf[end..];
}

fn addFile(path: []const u8, content: []const u8) void {
    const ino = create(ROOT, path, false) catch return;
    @memcpy(nodes[ino].data[0..content.len], content);
    nodes[ino].size = content.len;
}

pub fn init() void {
    for (&nodes) |*n| n.used = false;
    nodes[ROOT] = .{ .used = true, .is_dir = true, .parent = ROOT, .name_len = 0 };
    _ = create(ROOT, "etc", true) catch {};
    _ = create(ROOT, "home", true) catch {};
    _ = create(ROOT, "tmp", true) catch {};
    addFile("/etc/motd",
        \\Welcome to zigos! a tiny Unix-like OS written in Zig.
        \\Type 'help' to see available commands.
        \\
    );
    addFile("/etc/hostname", "zigos\n");
    addFile("/README",
        \\zigos: RISC-V 64 (QEMU virt) toy kernel.
        \\- M-mode kernel, U-mode shell (syscalls via ecall)
        \\- round-robin scheduler driven by CLINT timer
        \\- in-memory filesystem (this file lives in RAM)
        \\
    );
}
