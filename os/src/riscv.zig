//! RISC-V の CSR 操作と CLINT (タイマ) まわりの薄いラッパ
pub const MSTATUS_MPP_MASK: u64 = 3 << 11;
pub const MSTATUS_MPIE: u64 = 1 << 7;
pub const MIE_MTIE: u64 = 1 << 7;
pub const MIP_MTIP: u64 = 1 << 7;

// CLINT (Core Local Interruptor): mtime は 10MHz で増え続け、mtime >= mtimecmp でタイマ割り込み
const CLINT_MTIMECMP: usize = 0x0200_4000; // hart 0
const CLINT_MTIME: usize = 0x0200_BFF8;
pub const TIMER_HZ: u64 = 10_000_000;
pub const TICK_HZ: u64 = 100; // 10ms ごとに 1 tick
pub const TIMER_INTERVAL: u64 = TIMER_HZ / TICK_HZ;

pub inline fn csrRead(comptime name: []const u8) u64 {
    return asm volatile ("csrr %[r], " ++ name
        : [r] "=r" (-> u64),
    );
}

pub inline fn csrWrite(comptime name: []const u8, v: u64) void {
    asm volatile ("csrw " ++ name ++ ", %[v]"
        :
        : [v] "r" (v),
    );
}

pub inline fn csrSet(comptime name: []const u8, v: u64) void {
    asm volatile ("csrs " ++ name ++ ", %[v]"
        :
        : [v] "r" (v),
    );
}

pub inline fn wfi() void {
    asm volatile ("wfi");
}

pub fn readGp() u64 {
    return asm volatile ("mv %[r], gp"
        : [r] "=r" (-> u64),
    );
}

pub fn mtime() u64 {
    return @as(*volatile u64, @ptrFromInt(CLINT_MTIME)).*;
}

/// 今から delta 後にタイマ割り込みを起こす
pub fn setTimer(delta: u64) void {
    @as(*volatile u64, @ptrFromInt(CLINT_MTIMECMP)).* = mtime() + delta;
}

pub fn timerPending() bool {
    return csrRead("mip") & MIP_MTIP != 0;
}
