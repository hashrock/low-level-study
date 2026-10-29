//! UART 16550 ドライバ (QEMU virt では 0x10000000 に配置)
//! ポーリングのみ。割り込みは使わない。
const std = @import("std");

const base: usize = 0x1000_0000;
const RBR = 0; // 受信バッファ (読み込み)
const THR = 0; // 送信バッファ (書き込み)
const LSR = 5; // ラインステータス
const LSR_DATA_READY = 0x01;
const LSR_THR_EMPTY = 0x20;

fn reg(off: usize) *volatile u8 {
    return @ptrFromInt(base + off);
}

pub fn init() void {
    reg(1).* = 0x00; // IER: 割り込み無効
    reg(3).* = 0x03; // LCR: 8bit, パリティなし, 1 stop bit
    reg(2).* = 0x01; // FCR: FIFO 有効
}

pub fn putc(c: u8) void {
    while (reg(LSR).* & LSR_THR_EMPTY == 0) {}
    reg(THR).* = c;
}

/// 受信データがあれば 1 文字返す。なければ null
pub fn getc() ?u8 {
    if (reg(LSR).* & LSR_DATA_READY == 0) return null;
    return reg(RBR).*;
}

pub fn hasInput() bool {
    return reg(LSR).* & LSR_DATA_READY != 0;
}

pub fn puts(s: []const u8) void {
    for (s) |c| {
        if (c == '\n') putc('\r');
        putc(c);
    }
}

/// カーネル用 printf。固定長バッファに整形して出力する
pub fn print(comptime fmt: []const u8, args: anytype) void {
    var buf: [256]u8 = undefined;
    const s = std.fmt.bufPrint(&buf, fmt, args) catch "<print overflow>\n";
    puts(s);
}
