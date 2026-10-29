#!/bin/sh
# minidb のブラウザ版 (web/db/main.wasm) をビルドする。どこから実行してもよい。
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../../db"
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$here/main.wasm" ./cmd/wasm
echo "built $here/main.wasm ($(wc -c < "$here/main.wasm" | tr -d ' ') bytes)"
