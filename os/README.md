# zigos — Zig で書いた RISC-V 64 向けの小さな Unix 風 OS

学習用の最小カーネルです。QEMU の `virt` マシン上で動き、U-mode のシェルが
システムコール (ecall) を通じてメモリ上のファイルシステムを操作します。

- 動作確認環境: macOS (Apple Silicon), **Zig 0.17.0**, **QEMU 11.1.3**
- 全体で約 1200 行

## 動かし方

```sh
brew install zig qemu
./run.sh            # ビルドして QEMU を起動 (zig build run でも同じ)
```

**QEMU の終了**: `Ctrl-A` を押して離してから `X`。シェルで `halt` と打っても電源断できます
(`reboot` で再起動)。

非対話テスト (expect でコマンドを流し込み、最後に halt。QEMU は必ず終了します):

```sh
zig build && ./test.exp
```

最適化ビルドは `zig build --release` (ReleaseSmall)。既定は Debug (安全性チェック付き)。

### シェルのコマンド

```
help, echo, ls [DIR], cat FILE..., touch, mkdir, rm, cd, pwd,
ps, uptime, run PROG [&], clear, halt, reboot
```

`> file` / `>> file` でどのコマンドの出力もファイルにリダイレクトできます (例 `echo hi > a.txt`, `ls > list.txt`)。
行編集は Backspace と Ctrl-U (行消去) のみ。`run counter &` で 1 秒ごとに数を数えるプロセスを、
`run spin` で 3 秒間 CPU を使い続けるプロセスを起動できます。

## なぜ RISC-V + `-bios none` か

- RISC-V は特権命令・CSR が少なくシンプルで、トラップの仕組みが学びやすい。
- `-bios none` にすると OpenSBI を挟まず、リセット直後の **M-mode** から自分のコードが走る。
  タイマ (CLINT) も M-mode から直接触れるので、S-mode への委譲や SBI 呼び出しが要らず一番短く書ける。
- その代わり、カーネルは M-mode、ユーザは U-mode の 2 段構成で、S-mode (とページング) は使っていない。

## ファイル構成

| ファイル | 役割 |
|---|---|
| `build.zig` | ターゲット (riscv64-freestanding, 浮動小数点なし, medany) とリンカスクリプトの指定 |
| `src/linker.ld` | 0x80000000 に配置、BSS とカーネルスタックの位置を決める |
| `src/main.zig` | `_start` (ブートアセンブリ) と `kmain` (初期化) |
| `src/uart.zig` | UART 16550 ドライバ (ポーリング) |
| `src/riscv.zig` | CSR 読み書き、CLINT タイマ |
| `src/trap.zig` | トラップベクタ (アセンブリ)、割り込み/例外の振り分け、システムコール実装 |
| `src/proc.zig` | プロセステーブル、ラウンドロビンスケジューラ |
| `src/fs.zig` | メモリ上のファイルシステム |
| `src/syscall_nums.zig` | システムコール番号 (カーネル/ユーザ共有) |
| `src/user/ulib.zig` | ユーザ側のシステムコールラッパ (ミニ libc) |
| `src/user/shell.zig` | シェル (U-mode で動く) |
| `src/user/programs.zig` | ユーザプログラム一覧 (sh, counter, spin) |
| `test.exp` | 自動テスト |

## ブートの流れ

1. QEMU はリセット後、hart (CPU コア) を M-mode で `0x80000000` から実行する。
   リンカスクリプトで `.text.boot` セクション (`_start`) をそこに置いている。
2. `_start` (main.zig 内のアセンブリ):
   - `mhartid` が 0 以外のコアは `wfi` で眠らせる (今回は 1 コアのみ使う)
   - `sp` を `__stack_top` に設定 (C/Zig の関数を呼ぶにはスタックが必要)
   - `__bss_start`〜`__bss_end` をゼロクリア
   - `kmain` を呼ぶ
3. `kmain`:
   - UART 初期化、ramfs 初期化 (`/etc/motd` 等を作る)
   - `mtvec` にトラップベクタのアドレスを設定
   - **PMP** (Physical Memory Protection) を全領域 RWX 許可に設定。これをしないと U-mode は一切メモリにアクセスできない
   - CLINT の `mtimecmp` を設定し、`mie.MTIE` でタイマ割り込みを許可
   - `mstatus.MPP = U`, `MPIE = 1` にしておく (mret で U-mode に落ち、割り込み有効になる)
   - シェルプロセスを作り、`return_to_user` で TrapFrame のレジスタを復元して `mret` → シェルが U-mode で動き出す

## メモリマップ (QEMU virt)

```
0x0010_0000  sifive_test  ... 0x5555 を書くと電源断, 0x7777 で再起動 (halt/reboot)
0x0200_0000  CLINT        ... mtimecmp(0x0200_4000), mtime(0x0200_BFF8), 10MHz
0x0c00_0000  PLIC         ... (未使用)
0x1000_0000  UART 16550   ... コンソール
0x8000_0000  RAM 先頭 (128MiB)
  ├ .text     _start が先頭。カーネルとユーザプログラムのコードが同居
  ├ .rodata / .data
  ├ .bss      ramfs の inode 配列 (64 x 約2KiB)、プロセステーブル、ユーザスタック (16KiB x 8)
  └ カーネルスタック 16KiB (__stack_top)
```

ページングを使っていないので、物理アドレス = 仮想アドレス。ユーザプログラムもカーネルと同じ
ELF にリンクされた関数で、各プロセスは自分専用のスタックとレジスタ退避領域を持つだけです。

## トラップ処理とコンテキストスイッチ

U-mode で次のことが起きると CPU は M-mode に移り `mtvec` (= `trap_vector`) へ飛びます。

- `ecall` 命令 (システムコール) … `mcause = 8`
- タイマ割り込み (`mtime >= mtimecmp`) … `mcause = 割り込みビット | 7`
- 不正命令などの例外 … そのプロセスを kill

`trap_vector` (trap.zig) の動作:

1. `csrrw t6, mscratch, t6` で t6 と mscratch を交換。mscratch には常に「今のプロセスの TrapFrame のアドレス」が入っている
2. x1〜x31 と `mepc` を TrapFrame に保存
3. `sp` をカーネルスタックに切り替えて `trap_handler(frame)` (Zig) を呼ぶ
4. `trap_handler` は **再開すべき TrapFrame のポインタ** を返す
5. そのフレームを mscratch に入れ、レジスタを復元して `mret`

ステップ 4 で別プロセスのフレームを返せば、それがそのままコンテキストスイッチになります。
カーネル内では割り込み禁止 (M-mode では `mstatus.MIE=0`) で、システムコールはブロックせず
すぐ終わるので、カーネルスタックは 1 本で足ります。

### タイマとスケジューラ

- CLINT の `mtime` は 10MHz で増える。10ms 後に `mtimecmp` を設定 → 割り込み → `ticks += 1` して次を再設定
- タイマ割り込みのたびに `proc.schedule()` が次の実行可能プロセスを順番に選ぶ (ラウンドロビン、プリエンプティブ)
- プロセス状態: `runnable` / `sleeping` (`sleep` システムコール, wake_tick まで) / `wait_input` (キー入力待ち)
- 誰も走れないときはカーネル内で `wfi` して割り込みを待つ (アイドル)

### システムコール

`a7` に番号、`a0`〜`a3` に引数を入れて `ecall`。戻り値は `a0` (失敗は -1)。
カーネルは `mepc += 4` して ecall の次から再開させます。

| 番号 | 名前 | 説明 |
|---|---|---|
| 0 | exit | プロセス終了 |
| 1 | read(fd, buf, len) | fd 0〜2 はコンソール。入力がなければ `wait_input` にして **mepc を戻し、ecall を再実行させる** ことでブロッキングを実現 |
| 2 | write(fd, buf, len) | |
| 3 | open(path, len, flags) | O_CREAT / O_TRUNC / O_APPEND |
| 4 | close(fd) | |
| 5 | mkdir / 6 unlink / 7 chdir / 8 getcwd | |
| 9 | readdir(path, len, index, namebuf) | index 番目の子の名前・種類・サイズ |
| 10 | ps(buf, len) | プロセス一覧をテキストで返す |
| 11 | uptime / 12 sleep(ticks) | |
| 13 | spawn(name, len) | プログラム一覧から名前で探して新プロセス作成 |
| 14 | getpid / 15 yield / 16 halt(reboot) | |

## ファイルシステムの構造

`fs.zig` の `nodes: [64]Node` が全 inode。各 Node は

```
used, is_dir, parent (親 inode 番号), name[28], size, data[2048]
```

を持ちます。inode 0 がルート `/`。ディレクトリは子のリストを持たず、
**「parent が自分である inode」がそのディレクトリの中身** です (一覧は全 inode を走査)。
パス解決 (`lookup`) は `/` で区切って `.` / `..` を処理しながら子を探すだけ。
プロセスごとに `cwd` (inode 番号) とファイルディスクリプタ表 (inode + オフセット) を持ちます。
起動時に `/etc/motd`, `/etc/hostname`, `/README`, `/home`, `/tmp` を作ります。電源を切れば消えます。

## 制限事項

- **メモリ保護なし**: ページングを使わず PMP で全許可しているので、ユーザプロセスはカーネルのメモリも書き換えられる
- ユーザプログラムはカーネルと一緒にリンクされた関数 (ELF ローダなし)。`fork`/`exec`/`wait` なし (フォアグラウンド実行は ps をポーリングして待つ簡易版)
- UART はポーリング。入力待ちのときは最大 1 tick (10ms) 遅れて反応する。QEMU 起動直後に送られた入力は取りこぼすことがある
- ファイルは最大 2KiB、inode 64 個、名前 28 文字まで。パイプ `|` やクォート、Ctrl-C はない
- シングルコア。カーネル内は割り込み禁止で、システムコールは非リエントラント

## 次のステップ

1. **S-mode + Sv39 ページング**: OpenSBI 上で S-mode カーネルにし、プロセスごとのページテーブルでメモリを分離する
2. **ELF ローダ**: ユーザプログラムを別ビルドし、ramfs 上の ELF ファイルを読み込んで実行 (`exec`)
3. `fork` / `wait` / `kill`、パイプ
4. **PLIC + UART 割り込み** で入力をイベント駆動に
5. virtio-blk ドライバと永続ファイルシステム
6. マルチコア (各 hart の起動とスピンロック)
