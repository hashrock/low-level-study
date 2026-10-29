#!/bin/sh
# minilang のブラウザ版をビルドする: lang/cmd/wasm → web/lang/main.wasm
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../../lang"
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$here/main.wasm" ./cmd/wasm
echo "built $here/main.wasm ($(wc -c < "$here/main.wasm" | tr -d ' ') bytes)"
