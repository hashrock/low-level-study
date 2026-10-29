#!/bin/sh
# web/pipeline: 例題をコピーし、コンパイラ (rvcc / minicc) + アセンブラ (rvasm) + エミュレータ (rvemu) を
# まとめて 1 つの wasm にビルドする。どこから実行してもよい。
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

# 例題 (ページが fetch する)。index.json は「名前の一覧」だけ
ex="$here/examples"
rm -rf "$ex"
mkdir -p "$ex"
cp "$root"/cc/examples/*.c "$root"/cc/examples/*.out "$ex/"
cp "$root"/compiler/examples/*.lang "$root"/compiler/examples/*.out "$ex/"
cp "$root/cc/bench/fib25.c" "$root/cc/bench/fib25.lang" "$ex/"
{
  printf '['
  sep=''
  for f in "$root"/cc/examples/*.c "$root"/compiler/examples/*.lang "$root/cc/bench/fib25.c" "$root/cc/bench/fib25.lang"; do
    printf '%s"%s"' "$sep" "$(basename "$f")"
    sep=','
  done
  printf ']\n'
} > "$ex/index.json"

(cd "$here/wasm" && GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$here/main.wasm" .)
ls -l "$here/main.wasm"
