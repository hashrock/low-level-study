# minirt — Go で書いた小さなサンドボックス JavaScript ランタイム

JS エンジン本体には [goja](https://github.com/dop251/goja)（pure Go の ECMAScript 実装）を使い、
その**外側**、つまりイベントループ・非同期 I/O・パーミッション・リソース制限・CLI を自分で実装したものです。
Deno の設計（デフォルト全拒否 + `--allow-*` フラグ）を参考にしていますが、API 名は `Sandbox` 名前空間にしてあります。

```
go run . examples/hello.js
go run . examples/async_fs.js --allow-read=examples/data --allow-write=examples/data
```

- goja: `v0.0.0-20261028213108-85be1416108e`（2026-10-28）。Go 1.27 で動作確認。

---

## 1. エンジンとランタイムの責任分担

| 層 | 誰が実装 | 内容 |
|----|----------|------|
| エンジン | goja | 字句・構文解析、バイトコード VM、GC（Go の GC に乗る）、組み込みオブジェクト（`JSON`, `Math`, `Array` …）、Promise / async-await の意味論、**マイクロタスク(job)キュー** |
| ランタイム | このリポジトリ | グローバルに何を置くか、イベントループ（タイマー・マクロタスク）、goroutine での非同期 I/O、パーミッション検査、タイムアウト・スタック制限、エラー表示と終了コード、`require` |

ポイントは「エンジンは I/O を一切持たない」ことです。goja 単体では `fs` も `fetch` も `setTimeout` すらありません。
スクリプトが外界に触れられるのは、**ランタイムが Go の関数としてグローバルに差し込んだもの**だけです。
したがってサンドボックスの境界 = 「どのネイティブ関数を公開するか」+「その関数の中で何を検査するか」になります。

### ファイル構成

コアは `minirt/`（インポート可能なパッケージ）。CLI はその薄いラッパです。

| ファイル | 役割 |
|---|---|
| `main.go` | CLI 引数の解析、`Permissions` の構築、終了コード（`package main`） |
| `minirt/runtime.go` | goja の生成、各 API の組み込み、ブートストラップ JS、エラー変換、未処理 rejection 検出、`Load` / `Step` |
| `minirt/eventloop.go` | イベントループ本体（タイマーヒープ、完了キュー、タイムアウト、仮想時計とステップ実行、スナップショット） |
| `minirt/trace.go` | イベントループのトレース（タスク・マイクロタスク・タイマー・op・権限・console） |
| `minirt/permissions.go` | deny-by-default の検査、パス正規化（シンボリックリンク / `..` 解決） |
| `minirt/fs.go`, `memfs.go` | ファイルシステムの抽象 `FS`。`OSFS`（実ディスク）と `MemFS`（メモリ上、symlink もカーネルと同じ解決順序） |
| `minirt/net.go`, `net_http.go` | ネットワークの抽象 `Network`。`HTTPNetwork`（net/http、wasm ビルドからは除外）と `FakeNetwork`（決まった URL に遅延付きで応答） |
| `minirt/ops_fs.go` | `Sandbox.readTextFile` / `writeTextFile`（+ `Sync` 版） |
| `minirt/ops_net.go` | `fetch(url, {method, headers, body})` |
| `minirt/ops_env.go` | `Sandbox.env.get` / `has`（`Options.LookupEnv` 経由） |
| `minirt/ops_timers.go` | `setTimeout` / `setInterval` / `clear*` |
| `minirt/console.go` | `console.log/info/debug`（stdout）, `console.error/warn`（stderr）と簡易 inspect |
| `minirt/module.go` | CommonJS 風 `require("./x.js")`（読み取り権限で制御） |
| `playground/` | ブラウザ版の世界: 仮想 FS（`examples/` を同じ相対パスで `/app` に配置）、偽ネットワーク、偽の環境変数、仮想時計、トレース |
| `examples/embed.go` | 例のスクリプトとデータを `embed` する（ブラウザ版のプリセットと仮想 FS の中身） |
| `cmd/wasm/` | `GOOS=js` 用の main。`globalThis.minirt` に `load/step/run/files/examples/defaults` を公開 |

ブラウザ版は `web/runtime/`（`web/runtime/build.sh` でビルド）。

### 公開 API

```js
console.log / info / debug / error / warn
setTimeout / clearTimeout / setInterval / clearInterval / queueMicrotask
fetch(url, { method, headers, body }) -> Promise<{ status, statusText, ok, url, headers, text(), json() }>
require("./lib.js")                       // CommonJS、--allow-read が必要
Sandbox.readTextFile(path)  -> Promise<string>
Sandbox.writeTextFile(path, data) -> Promise<void>
Sandbox.readTextFileSync(path) / Sandbox.writeTextFileSync(path, data)
Sandbox.env.get(name) / Sandbox.env.has(name)
Sandbox.args                               // `--` 以降、またはスクリプト名の後の非フラグ引数
Sandbox.permissions.query("read" | "write" | "net" | "env", target) -> boolean
Sandbox.errors.PermissionDenied / NotFound / TimedOut   // instanceof で判定可能
```

ES Modules（`import`）とトップレベル `await` は未対応です。goja はこのバージョンでは ESM ローダを公開 API として
持っていないため、代わりに CommonJS 風の `require` を約 70 行で実装しています（`node_modules` 探索なし、`.js` のみ）。

---

## 2. イベントループ

**黄金律: `goja.Runtime` に触ってよいのはループ goroutine だけ。** goja はスレッドセーフではありません
（唯一の例外は `vm.Interrupt`）。ワーカー goroutine は JS の値を一切見ず、Go の値（`string`, 構造体）を計算して
「ループ上で実行してほしいクロージャ」を channel に送り返すだけです。

```
                        ┌──────────────────────── loop goroutine (goja を触れる唯一のスレッド) ──────────────────────┐
                        │                                                                                              │
  main script ─────────▶│ ① マクロタスク実行  ──▶ goja が終了時に job キューを全部流す (= マイクロタスク drain)           │
                        │        │                    Promise.then / await の継続 / queueMicrotask                    │
                        │        ▼                                                                                     │
                        │ ② afterTask: 未処理 rejection があれば致命的エラー                                           │
                        │        │                                                                                     │
                        │        ▼                                                                                     │
                        │ ③ 期限切れタイマーを順に実行（1 個ごとに①②）                                                 │
                        │ ④ completions に溜まっている完了通知を全部実行（1 個ごとに①②）                               │
                        │ ⑤ タイマーも実行中 op も無ければ終了                                                         │
                        │ ⑥ select { 次のタイマー期限 | completions | タイムアウト } で眠る ──▶ ③へ                     │
                        └──────────────▲───────────────────────────────────────────────────────────────────────────────┘
                                       │ completions chan func() error
             ┌─────────────────────────┴──────────────────────────┐
             │ worker goroutines (os.ReadFile, http.Client.Do …)  │   ← JS の値は一切触らない
             └────────────────────────────────────────────────────┘
```

### マイクロタスクはどこで流れるのか

goja は Promise のジョブキューを内部に持ち、**JS → Go に最外周で制御が戻るとき**（`Runtime.leave()`）に
キューが空になるまで実行します。ランタイムが実行するマクロタスク（メインスクリプト、タイマーコールバック、
op 完了時の `resolve()` 呼び出し）は全部この「最外周の呼び出し」なので、
「マクロタスクを 1 個 → マイクロタスクを全部」という HTML / Node と同じ順序が自然に得られます。
`examples/timers.js` の出力がこれを確認しています。

### 非同期 op の流れ（`Sandbox.readTextFile` の場合）

1. ループ上で引数検査とパーミッション検査。拒否ならその場で rejected な Promise を返す。
2. `vm.NewPromise()` で Promise と resolve/reject を作り、`inFlight++`。
3. goroutine で `open(O_NOFOLLOW)` + 読み取り（JS 値には触れない）。
4. 結果を閉じ込めたクロージャを `completions` に送信。
5. ループがそれを受け取り `inFlight--`、ループ上で `resolve(vm.ToValue(s))` → 続く `await` がマイクロタスクとして走る。

### タイマー

- 二分ヒープ（`container/heap`）で期限順、同時刻は登録順（`seq`）。
- 1 回のパスでは「パス開始時点で存在したタイマー」だけを実行するので、`setTimeout(f, 0)` を無限に積んでも
  I/O 完了が飢餓状態になりません。
- `setInterval(f, 0)` は 1ms に切り上げ（ビジーループ防止）。

---

## 3. パーミッションモデル

| フラグ | 許可する操作 | 値なし |
|---|---|---|
| `--allow-read=DIR,...` | `readTextFile(Sync)`, `require` | 全ファイル |
| `--allow-write=DIR,...` | `writeTextFile(Sync)` | 全ファイル |
| `--allow-net=HOST[:PORT],...` | `fetch` | 全ホスト |
| `--allow-env=NAME,...` | `Sandbox.env.*` | 全変数 |
| `--timeout=5s` | 壁時計の上限（デフォルト 30s、`0` で無制限） | |
| `--max-stack=N` | JS 呼び出しの深さ上限（デフォルト 10000） | |

何も指定しなければ**全部拒否**です。拒否は `Sandbox.errors.PermissionDenied` 例外として JS に投げられます
（非同期 API は rejected Promise）。メインスクリプト自体の読み込みだけは暗黙に許可されます。

### パスの正規化（`permissions.go`）

`--allow-read=./data` で `./data/../secret` や `./data/link -> /etc` を通さないため:

1. 許可ディレクトリは**起動時に** `filepath.Abs` + `filepath.EvalSymlinks` で正規化（存在しないパスは許可できない）。
2. 要求されたパスは `cwd + "/" + p` として（`filepath.Join` は使わない。`Join` は `Clean` を呼び、
   `link/..` を**字句的に**潰してしまうため）、`EvalSymlinks` でカーネルと同じ意味論で解決する。
   つまり `data/link_out/../a.txt`（`link_out -> secret`）は `data/a.txt` ではなく `root/a.txt` になる。
3. 存在しない末尾（書き込み先の新規ファイル）は、存在する最長の親を解決してから名前を付け足す。
   存在しない要素の後の `..` は拒否（字句的に潰すと検査が甘くなる方向にしか働かないため）。
4. 「正規化済みパスが許可ディレクトリと等しい、または `dir + "/"` で始まる」かを判定
   （`/data` と `/data2` の取り違えを防ぐため区切り文字まで含めて比較）。
5. 実際の `open` は**検査で得た正規化済みパス**に対して `O_NOFOLLOW` 付きで行う
   （検査と open の間に最終要素がシンボリックリンクに差し替えられたら失敗させる）。

ネットワークはホスト名（+任意でポート）の完全一致です。サブドメインは暗黙には許可しません。
**リダイレクトの各ホップでも再検査**します（許可ホストが内部アドレスへ 302 するのを防ぐ）。
スキームは `http` / `https` のみ（`file://` 等は TypeError）。

---

## 4. リソース制限

| 制限 | 方法 | 効き方 |
|---|---|---|
| 壁時計 | `time.AfterFunc` → `vm.Interrupt` + ループの `interrupt` channel を close + `context` cancel | `while(true){}` も、`setInterval` で待機中のアイドルループも、実行中の HTTP リクエストも止まる。終了コード 124 |
| コールスタック深さ | `vm.SetMaxCallStackSize(N)` | 超えると goja の `*StackOverflowError`。**catch 不可**（スクリプトが握りつぶせない） |
| 1 回の読み取り | `MaxReadBytes = 64MiB` | ファイル読み取りの上限 |
| レスポンス | `MaxResponseBytes = 16MiB` | `fetch` の本文の上限 |
| メモリ全体 | **制限なし** | 下記参照 |

### メモリ制限について（正直に）

goja は JS のオブジェクトを普通の Go のヒープに確保し、Go の GC で回収します。
エンジンにはヒープ使用量を数えて上限で止める仕組みがありません（V8 の isolate ヒープ上限のようなものは無い）。
そのため `"x".repeat(2**28)` や巨大配列を作り続けるスクリプトは**ホストプロセスごと OOM にできます**。

取り得る対策と、ここで採用していない理由:

- `debug.SetMemoryLimit` … GC を頻繁に走らせる**ソフト**目標であり、上限を超えた確保を拒否しない。
  「制限している」と言うと嘘になるので入れていません。
- `runtime.ReadMemStats` を定期的に見て `Interrupt` … 検出は事後的で、1 回の巨大確保は防げない。
  しかも同一プロセス内の他の goroutine の分も混ざる。
- **本当に必要ならプロセス分離**: ランタイムを子プロセスとして起動し、`setrlimit(RLIMIT_AS)`、
  Linux なら cgroups の `memory.max` をかける。これが唯一信頼できる方法です。

### 仮想時計とトレース（`--virtual-time`, `--trace`）

- `Options.VirtualClock`（CLI では `--virtual-time`）: `Date.now()` とタイマーは仮想時刻。やることが無ければ次の期限まで一気に進む。
  op の処理は開始時にその場で実行し、結果を「開始時刻 + 遅延（`FS.Latency()` / `FetchResponse.Latency`）」に届ける。
  タイマーと op 完了は (期限, 登録順) の一つの順序で選ぶので、何度実行しても同じ順序。`Runtime.Step()` は
  マクロタスク 1 個 + そのマイクロタスクだけを実行する。`--timeout` は仮想時刻の上限になり、加えて 1 ステップごとの
  実時間ウォッチドッグとしても働く（`for(;;){}` 対策）。
- `Options.Trace`（CLI では `--trace` で stderr に JSON Lines）: `task.start / task.body.end / task.end`,
  `micro.register / micro.queued / micro.run / micro.done / micro.drop`, `timer.set / fire / clear`,
  `op.start / op.done`, `perm`（正規化後のパス付き。スクリプトには見せない）, `console`, `clock`, `exit`。

**マイクロタスクの観測は近似です。** goja のジョブキューには「積まれた」ことを知るフックが無いので、
`AsyncContextTracker`（反応の登録時 `Grab` と、ハンドラ実行の前後 `Resumed`/`Exited`）と
`Promise.prototype.then` の差し替えで観測しています。

- `queueMicrotask` と解決済み Promise への `.then`: 登録 = キュー投入なので正確（`state: "queued"`）。
- 未解決 Promise への `.then`: `"waiting"`。解決したことは次に制御が戻った時点（次のジョブの直前・タスク本体の終わり）で
  `micro.queued` として報告する。FIFO 上の位置は正しいが瞬間は遅れる。
- `await`: goja は内部の then を直接呼ぶので待っている値が見えない。`"unknown"` のまま、走った時点で確定。
  位置情報も関数の先頭しか取れないので関数名だけ。再開後の `await` は「#N の続き」と表示。
- ハンドラを持たない反応（`.catch(f)` の成功側など）はタスク終了時に `micro.drop`。thenable 解決などの内部ジョブは見えない。
- トレース有効時だけ `Promise.prototype.then` と `queueMicrotask` を差し替え、メインスクリプト末尾に本体終了の目印呼び出しを足す。
  トレース無効（通常の CLI）では何も変えない。

---

## 5. 脅威モデルと限界

### 守っているもの

- スクリプトは許可なしにファイル・ネットワーク・環境変数に触れない（そもそも API が Go 側の検査を必ず通る）。
- `../` やシンボリックリンクで許可ディレクトリの外に出られない（カーネルと同じ解決順序で検査）。
- `process`, `Deno`, `require("fs")` のような「暗黙の能力」は存在しない。
- 無限ループ・無限タイマー・応答しない HTTP はタイムアウトで止まる。無限再帰はスタック上限で止まる。
- リダイレクトで許可外のホストへ行けない。

### 守っていないもの（既知の限界）

- **メモリ枯渇**（上記）。
- **CPU 使用量そのもの**: 制限は壁時計だけ。命令数の上限はない。
- **Go のネイティブ処理中の割り込み**: `vm.Interrupt` は JS のバイトコード実行中にしか効かない。
  巨大な `String.prototype.repeat`、`JSON.parse`、正規表現の破滅的バックトラッキングなど、
  組み込み関数の中に居る間はタイムアウトが遅れる（終わって JS に戻った時点で止まる）。
  `readTextFileSync` で巨大ファイルを読んでいる間も同様。
- **TOCTOU**: 検査と `open` の間に**途中のディレクトリ**がシンボリックリンクに差し替えられる競合は防げない
  （`O_NOFOLLOW` が守るのは最終要素のみ）。完全に防ぐには Linux の `openat2(RESOLVE_BENEATH)` や
  許可ディレクトリの fd を起点にした `openat` 連鎖、あるいは chroot / mount namespace が必要。
- **ハードリンク**: 許可ディレクトリ内に、外部ファイルへのハードリンクが（他者によって）置かれていれば読み書きできる。
  パスベースの検査の本質的な限界。
- **ネットワークはホスト名ベース**: `--allow-net=example.com` の名前解決結果が `127.0.0.1` や社内 IP でも通る
  （DNS rebinding）。IP の検査や名前解決結果の固定はしていない。
- **同一プロセス**: エンジン（goja）やこのランタイムにバグがあれば、そのままホストプロセスの権限で動く。
  OS レベルの隔離（seccomp, sandbox-exec, コンテナ）は無い。
- **DoS 系**: 同時に走らせる op の数、`console.log` の出力量、タイマーの数に上限はない。
- **サイドチャネル**: `Date.now()` などのタイマー精度は落としていない。
- Windows は未対応（`syscall.O_NOFOLLOW` を使っているため）。

---

## 6. 動かし方と例

```sh
go run . examples/hello.js a b
go run . examples/timers.js
go run . examples/async_fs.js --allow-read=examples/data --allow-write=examples/data
go run . examples/sandbox_escape.js --allow-read=examples/data --timeout=2s   # 終了コード 124
go run . examples/fetch.js --allow-net=example.com

go run . examples/event_loop_quiz.js --virtual-time --trace 2>trace.jsonl

go vet ./... && go test -race ./...
GOOS=js GOARCH=wasm go vet ./...
```

`examples/data/link_to_etc` は `/etc` へのシンボリックリンクで、`sandbox_escape.js` のシンボリックリンク脱出テストに使っています。

### `examples/timers.js` の出力

```
1 sync
2 sync end
3 micro (promise.then)
4 micro (queueMicrotask)
5 micro (after await)
6 timeout 0
7 micro inside timeout (runs before next timer)
8 timeout 0 (second)
9 timeout 10
interval tick 1
interval tick 2
interval tick 3
done
```

### `examples/sandbox_escape.js` の出力

```
blocked  read /etc/passwd: PermissionDenied: Requires read access to "/etc/passwd", run again with the --allow-read flag
blocked  ../ escape: PermissionDenied: ...
blocked  deep ../ escape: PermissionDenied: ...
blocked  symlink escape: PermissionDenied: ...
blocked  require outside: PermissionDenied: ...
blocked  write (no --allow-write): PermissionDenied: ...
blocked  fetch without --allow-net: PermissionDenied: Requires net access to "example.com", run again with the --allow-net flag
blocked  file:// fetch: TypeError: fetch: unsupported scheme "file"
blocked  env without --allow-env: PermissionDenied: ...
blocked  node-style process: ReferenceError: process is not defined
blocked  Function constructor reaching globals: TypeError: require("fs"): only relative or absolute file paths are supported
now spinning forever; --timeout must stop this...
error: execution timed out
```

### 終了コード

| コード | 意味 |
|---|---|
| 0 | 正常終了 |
| 1 | 捕捉されない例外 / 未処理の rejection / スタックオーバーフロー |
| 2 | 引数エラー |
| 124 | タイムアウト |

### テスト

- `minirt/memfs_test.go`: `MemFS` が permissions_test と同じケース（`link/..` のカーネル意味論など）で実ディスクと同じ判定をすること。
- `minirt/virtual_test.go`: 仮想時計（10 分のタイマーが一瞬で終わる、仮想タイムアウト、busy loop のウォッチドッグ、op 遅延と
  タイマーの順序）、トレースを有効にしても順序が変わらないこと、トレースの形、正規化パスがスクリプトに漏れないこと、
  偽ネットワークのリダイレクト再検査（実時間モード、ワーカー goroutine 上）。
- `playground/playground_test.go`: ブラウザ版の世界で `timers.js` の出力が CLI と同一、`async_fs.js`・`sandbox_escape.js`
  （11 件すべて拒否・正規化パス・終了コード 124）・偽 fetch・クイズ例・1 ステップ = 1 タスク。
- `permissions_test.go`: `..` 脱出、シンボリックリンク脱出、`link/..` のカーネル意味論、`data` と `data2` の取り違え、
  新規ファイル書き込み、デフォルト拒否、ネットワークのホスト/ポート照合。
- `eventloop_test.go`: マイクロタスク vs タイマーの順序、遅延と clear、goroutine op の完了順序、
  無限ループ / アイドル interval のタイムアウト、catch 不可のスタック上限、未処理例外・rejection、
  JS から見たデフォルト拒否、ローカル `httptest` サーバでの `fetch` 許可と**リダイレクト先の再検査**（オフラインで動く）。
