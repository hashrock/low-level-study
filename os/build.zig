// Zig 0.17.0 用ビルドスクリプト
const std = @import("std");

pub fn build(b: *std.Build) void {
    // RISC-V 64bit / OS なし (freestanding)。浮動小数点は使わない(コンテキストスイッチで保存しないため)
    const target = b.resolveTargetQuery(.{
        .cpu_arch = .riscv64,
        .os_tag = .freestanding,
        .abi = .none,
        .cpu_features_sub = std.Target.riscv.featureSet(&.{ .d, .f }),
    });
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseSmall });

    const kernel_mod = b.createModule(.{
        .root_source_file = b.path("src/main.zig"),
        .target = target,
        .optimize = optimize,
        .code_model = .medium, // 0x80000000 付近に置くので medany
    });
    const kernel = b.addExecutable(.{ .name = "kernel.elf", .root_module = kernel_mod });
    kernel.setLinkerScript(b.path("src/linker.ld"));
    b.installArtifact(kernel);

    // zig build run で QEMU 起動
    const run = b.addSystemCommand(&.{
        "qemu-system-riscv64", "-machine", "virt", "-bios", "none", "-m", "128M", "-nographic", "-kernel",
    });
    run.addArtifactArg(kernel);
    run.step.dependOn(b.getInstallStep());
    b.step("run", "QEMU で起動する").dependOn(&run.step);
}
