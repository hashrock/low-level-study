#!/bin/sh
# Build web/runtime/main.wasm (minirt + goja for the browser).
# Works from any directory.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../runtime" && pwd)
cd "$root"
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$here/main.wasm" ./cmd/wasm
ls -l "$here/main.wasm" | awk '{printf "web/runtime/main.wasm: %.1f MB\n", $5/1048576}'
