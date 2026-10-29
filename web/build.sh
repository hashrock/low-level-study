#!/bin/sh
# 全ページの wasm をビルドする。使い方: ./web/build.sh  →  python3 -m http.server -d web 8000
set -e
cd "$(dirname "$0")"
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" shared/wasm_exec.js
for d in os pipeline lang db net runtime; do
  if [ -x "$d/build.sh" ]; then
    echo "== $d"
    "$d/build.sh"
  else
    echo "== $d (build.sh なし、スキップ)"
  fi
done
ls -la */main.wasm 2>/dev/null | awk '{printf "%8.1f MB  %s\n", $5/1048576, $9}'
