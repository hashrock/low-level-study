#!/bin/sh
# web/os: zigos カーネルをビルドして kernel.elf をコピーし、エミュレータを wasm にビルドする。
# どこから実行してもよい。
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

# カーネル (Zig)。DWARF デバッグ情報は要らないので落とす (シンボル表は逆アセンブル表示に使うので残す)
(cd "$root/os" && zig build)
if ! zig objcopy --strip-debug "$root/os/zig-out/bin/kernel.elf" "$here/kernel.elf" 2>/dev/null; then
  cp "$root/os/zig-out/bin/kernel.elf" "$here/kernel.elf"
fi

# エミュレータ (Go → wasm)
(cd "$root/emu" && GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$here/main.wasm" ./cmd/wasm)

ls -l "$here/main.wasm" "$here/kernel.elf"
