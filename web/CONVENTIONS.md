# web/ 共通ルール（ブラウザ版の作り方）

各プロジェクトを Go → WebAssembly でブラウザ上に載せ、内部状態を可視化する。

## ディレクトリ

```
web/
  shared/style.css      共通スタイル（色は必ずここの CSS 変数を使う。ダークモード対応）
  shared/wasm_exec.js   Go 付属のローダ（コピー済み。編集しない）
  index.html            ポータル（メインセッションが作る）
  build.sh              全ページのビルド（メインセッションが作る。各ページの build.sh を順に呼ぶ）
  <name>/index.html     各ページ（HTML/CSS/JS はこのファイルにインライン。外部ライブラリは使わない）
  <name>/build.sh       このページの wasm をビルドする（web/<name>/ から実行しても、どこから実行しても動くこと）
  <name>/main.wasm      ビルド成果物（git には入れない）
  <name>/*.elf 等       ページが読み込むデータ（必要なら）
```

- wasm の main パッケージは**各プロジェクトのモジュール内**に置く: 例 `emu/cmd/wasm/main.go`（先頭に `//go:build js && wasm`）。
  複数モジュールをまたぐ場合だけ `web/<name>/wasm/go.mod` に `replace` で置く。
- ビルド: `GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/<name>/main.wasm ./cmd/wasm`
- HTML から読み込む: `<link rel="stylesheet" href="../shared/style.css">`、`<script src="../shared/wasm_exec.js"></script>`、wasm は `main.wasm`（相対パス）。
- Go 1.27。標準ライブラリ + `syscall/js` のみ（runtime は goja 可）。

## Go 側の作法

- コアのロジックを `package main` から**インポート可能なパッケージ**に切り出す（例 `emu/rv`）。CLI (`go run .`) と既存テストは今まで通り動くこと。`go vet ./...` / `go test ./...` が通ること。
- JS へは `js.Global().Set("<name>", ...)` で小さな API を公開する（関数は JSON 文字列か素朴な値を返す）。重い処理はチャンク実行してブラウザを固まらせない（例: 1 フレームで最大 N 命令）。
- 内部状態は「スナップショットを JSON で返す」関数を用意し、JS 側で描画する。差分（前回から変わった値）を強調表示する。

## UI の作法

- 本文・説明は日本語。`<title>` は 2〜4 語の名前。
- ヘッダ: `<header class="top"><a href="../">← 一覧</a><h1>…</h1> …</header>`
- 共通クラス: `.panel` `.grid` `.toolbar` `.terminal` `table.kv` `.changed`（変化した値）`.current`（現在位置）`button.primary`。
- 色は CSS 変数（`--accent`, `--hl`, `--c1`..`--c6` など）のみ。直書きの色は使わない（`.terminal` は既に定義済み）。
- 横幅 360px のスマホでも横スクロールしないこと（パネルの中身はパネル内でスクロール）。
- wasm 読み込み中は `.loading` を表示。エラーは画面に出す。
- 外部 CDN やフォントは使わない。localStorage を使うなら try/catch で囲む。

## 動作確認

- `python3 -m http.server -d web 8000` で配信して確認する。
- ヘッドレスブラウザ: Playwright のブラウザが `~/Library/Caches/ms-playwright` にある。スクリプトや node_modules は**スクラッチパッドに置く**（リポジトリに入れない）。
  ページを開き、操作し、コンソールエラーがないこと・期待するテキストが出ることを確認し、スクリーンショット（ライト/ダーク、幅 1280 と 390）を撮って自分で見る。
- `GOOS=js GOARCH=wasm` 専用コードのロジックは、なるべく通常の Go テストで検証できる形にする。
