#!/bin/sh
# web/hw: hw/ の Verilog CPU (単一サイクル版・5 段パイプライン版) で hw/demos/*.s を
# Icarus Verilog で実際にシミュレーションし、波形を traces/*.json にする (wasm は使わない)。
# どこから実行してもよい。
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

for t in iverilog vvp; do
  if ! command -v "$t" >/dev/null 2>&1; then
    echo "web/hw/build.sh: $t が見つかりません。Icarus Verilog をインストールしてください" >&2
    echo "  macOS: brew install icarus-verilog   Ubuntu: sudo apt-get install -y iverilog" >&2
    exit 1
  fi
done

rm -rf "$here/traces"
(cd "$root/hw" && go run ./tools/trace2json -o "$here/traces")
du -sh "$here/traces"
