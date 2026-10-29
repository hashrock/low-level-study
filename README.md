# 2026-low-level

ローレベルの勉強用に、「少ないコードで、ちゃんと動くもの」を一通り作ったリポジトリです。
各ディレクトリの `README.md` に仕組みの解説（データ配置・状態遷移・命令形式など）があります。

| ディレクトリ | 言語 | 中身 |
|---|---|---|
| [`db/`](db/) | Go | SQL サブセット + ページャ + B+tree の RDB |
| [`lang/`](lang/) | Go | JS 風だが暗黙変換・`undefined`・`this` の無い言語。Pratt パーサ + バイトコード VM |
| [`os/`](os/) | Zig | RISC-V (QEMU virt) 用ミニ OS。U-mode シェル・プリエンプティブスケジューラ・RAM FS |
| [`runtime/`](runtime/) | Go + goja | 自前イベントループ + deny-by-default サンドボックスの JS ランタイム |
| [`emu/`](emu/) | Go | RV64IMAC エミュレータ。`os/` のカーネルも起動できる |
| [`asm/`](asm/) | Go | RISC-V アセンブラ + リンカ（本物の ELF を出力） |
| [`compiler/`](compiler/) | Go | `lang` → RISC-V ネイティブコンパイラ（`asm` と `emu` を使う） |
| [`cc/`](cc/) | Go | C サブセット → RISC-V コンパイラ（静的型。`compiler/` との比較用、libc は自分自身でコンパイル） |
| [`net/`](net/) | Go | ユーザ空間 TCP/IP スタック。仮想ケーブル + pcap 出力 |

必要なもの: Go 1.27、Zig 0.17、QEMU（`brew install go zig qemu`）。

```
          ┌──────────┐  .s
 .lang ──▶│ compiler │──────┐
          └──────────┘      ▼
          ┌──────────┐  .s ┌──────┐  ELF  ┌──────┐
 .c ─────▶│    cc    │────▶│ asm  │──────▶│ emu  │◀── os/ の kernel.elf
          └──────────┘     └──────┘       └──────┘
                                          ↑ QEMU の代わり
 .lang ──▶ lang（バイトコード VM）
```

---

## ブラウザで動かす（内部状態を覗く）

全部 Go → WebAssembly にして、ブラウザ上で動かしながら中身を可視化できます。

```sh
./web/build.sh                      # 全ページの wasm をビルド（os は zig build も実行）
python3 -m http.server -d web 8000  # → http://localhost:8000/
```

| ページ | 見られるもの |
|---|---|
| `os/` | zigos を自作 CPU で起動。レジスタ・CSR・特権モード、逆アセンブル、トラップ／syscall ログ、メモリ |
| `pipeline/` | C / minilang → アセンブリ → 機械語のビット → ELF → 実行。3 カラムの相互ハイライト、ソース行単位のステップ、命令数比較 |
| `lang/` | バイトコード VM のステップ実行。値スタック・フレーム・クロージャが共有するセル |
| `db/` | B+tree の図と分割のハイライト、4KB ページの hex、ページキャッシュ、主キー検索 vs 全件走査 |
| `net/` | 2 ノード間のパケットをシーケンス図で。ロス率・遅延、層ごとの分解、TCP 状態遷移、pcap ダウンロード |
| `runtime/` | イベントループ（タスク・microtask・timer・非同期 op）、実行順クイズ、パーミッションログ |

ページの作り方の決まりごとは [`web/CONVENTIONS.md`](web/CONVENTIONS.md)。

---

## おすすめの遊び方

### 1. 自作スタックを全部通す（compiler → asm → emu）

自作言語のプログラムが、自作アセンブラで ELF になり、自作 CPU で動くのを見る。

```sh
cd compiler
go run . emit examples/fib.lang | less          # 生成アセンブリ（ソース行・日本語コメント付き）
go run . build examples/fib.lang -o /tmp/fib     # ../asm のライブラリ (rvasm) で ELF を作る
cd ../emu
go run . run --stats /tmp/fib                    # 実行命令数と MIPS が出る
go run . run --trace /tmp/fib | head -50         # 1 命令ずつのトレース
```

やってみること:
- 同じ `fib.lang` を `cd lang && go run . -dis examples/fib.lang` でバイトコードにして、ネイティブコードと見比べる
- `fib(25)` の実行命令数（約 1700 万）を、`compiler/minicc/codegen.go` をいじって減らしてみる（レジスタ割り当て、インライン化…）
- 同じ fib を C で書いた `cc/` 版と比べる（静的型だと約 490 万命令。タグ検査やランタイム呼び出しが消える）:
  ```sh
  cd cc
  go run . emit -O0 examples/fib.c | less        # -O0 / -O1 / -O2 で生成コードを見比べる
  go run . run --stats examples/fib.c
  ```

### 2. 自作 OS を自作 CPU で起動する

QEMU の代わりに `emu` で `os/` を起動する。

```sh
cd os && zig build                               # zig-out/bin/kernel.elf
./run.sh                                         # まずは本物の QEMU で（終了は halt か Ctrl-A X）
cd ../emu
go run . boot ../os/zig-out/bin/kernel.elf       # 同じカーネルを自作エミュレータで
go run . boot --trace ../os/zig-out/bin/kernel.elf 2>&1 | head -200   # ブートの最初の 200 命令
```

シェルで試すコマンド: `ls`, `cat /etc/motd`, `echo hi > a.txt`, `run counter &`, `run spin`, `ps`, `uptime`, `halt`

やってみること:
- `--trace` で `ecall`（システムコール）や `mret` の瞬間に特権モードが `U`↔`M` と切り替わるのを探す
- `os/src/user/` に新しいユーザプログラムやシェルコマンドを追加する
- `os/README.md` の「次のステップ」（Sv39 ページング、ELF ローダ）に挑戦し、`emu` 側も合わせて拡張する

### 3. アセンブリを手で書く

```sh
cd asm
go run . build examples/main.s examples/lib.s -o /tmp/prog    # 複数ファイルをリンク
cd ../emu && go run . run --trace /tmp/prog | head -30
```

やってみること:
- `asm/examples/hello.s` をコピーして、FizzBuzz や文字列反転をアセンブリで書く（syscall は write=64, read=63, exit=93）
- `li a0, 0x123456789abcdef0` が何命令に展開されるか `--trace` で確認する
- `asm` が出す `.o` を `go run . as x.s -o x.o` で作り、再配置情報（`R_RISCV_*`）を眺める

### 4. 言語を設計する（lang）

```sh
cd lang
go run .                                         # REPL
go run . examples/no_coercion.lang               # 1 + "a" がエラーになる
go run . -dis examples/closures.lang             # クロージャがどうバイトコードになるか
```

やってみること:
- `lang/README.md` の「JS との違い」の表を見て、自分なら変える仕様を 1 つ変えてみる
- 組み込み関数 `map`/`filter` を VM 側に追加する（今はユーザ関数を呼べない制約がある）
- 同じ機能を `compiler/` にも入れて、VM 版とネイティブ版の実装の差を体感する

### 5. DB の中身を覗く

```sh
cd db
go run . /tmp/my.db
```
```sql
CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO users VALUES (1, 'alice'), (2, 'bob');
SELECT * FROM users WHERE id = 2;
.schema
```

やってみること:
- `.exit` して `xxd /tmp/my.db | less` でページ 0（カタログ）と B+tree のページを読む（レイアウトは `db/README.md`）
- 大量に INSERT して木が何段になるか確かめる。行サイズを変えると分割のタイミングが変わる
- 次のステップ: WAL を足してクラッシュ安全にする、セカンダリインデックスを付ける

### 6. TCP をパケット単位で観察する（net）

ターミナルを 2 つ使う。

```sh
# ターミナル 1: サーバ（TCP/UDP echo :7, HTTP :80）
cd net
go run . node --mac 02:00:00:00:00:01 --ip 10.0.0.1 \
    --wire-listen 127.0.0.1:9001 --wire-peer 127.0.0.1:9002 --pcap /tmp/a.pcap serve

# ターミナル 2: クライアント
cd net
B="go run . node --mac 02:00:00:00:00:02 --ip 10.0.0.2 --wire-listen 127.0.0.1:9002 --wire-peer 127.0.0.1:9001"
$B ping 10.0.0.1 3
$B -v tcp-echo 10.0.0.1 7 "hello"                # 状態遷移と各セグメントのログ
$B --loss 0.1 -v tcp-bulk 10.0.0.1 7 100000      # 10% 落として再送を観察
$B http-get 10.0.0.1 80 /hello
```

やってみること:
- `/tmp/a.pcap` を Wireshark で開いて、3-way handshake と FIN の流れを確認する
- `--loss` の値を上げて、タイムアウト再送と fast retransmit の違いを `-v` のログで見る
- 次のステップ: 輻輳制御（スロースタート）を入れて、`--loss` 下でのスループットを比べる

### 7. サンドボックスを破ってみる（runtime）

```sh
cd runtime
go run . examples/timers.js                      # microtask と timer の実行順
go run . --allow-read=examples/data --timeout=2s examples/sandbox_escape.js
```

やってみること:
- `sandbox_escape.js` に新しい脱出手段を書き足して、全部ブロックされるか試す
- `runtime/README.md` の「守れないもの」（メモリ上限・TOCTOU・DNS rebinding）を実際に再現してみる
- `timers.js` の出力順をブラウザや Node.js と比べる

---

## テスト

```sh
for d in db lang runtime emu asm compiler cc net web/pipeline/wasm; do (cd $d && go test ./...); done
cd os && zig build && ./test.exp                 # QEMU 上でシェルを自動操作
```
