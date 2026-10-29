#!/bin/sh
# net (mininet) のブラウザ版をビルドする。どこから実行してもよい。
set -e
here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/../../net"
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$here/main.wasm" ./cmd/wasm
ls -l "$here/main.wasm" | awk '{printf "web/net/main.wasm  %.1f MB\n", $5/1048576}'
