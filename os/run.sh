#!/bin/sh
# ビルドして QEMU で起動する。終了は Ctrl-A を押してから X
set -e
cd "$(dirname "$0")"
zig build
exec qemu-system-riscv64 -machine virt -bios none -m 128M -nographic \
    -kernel zig-out/bin/kernel.elf
