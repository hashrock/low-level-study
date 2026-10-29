# rvemu — 学習用の小さな RISC-V エミュレータ (Go)

RV64IMAC + Zicsr を実装した、読むためのエミュレータです。標準ライブラリのみ (Go 1.27)。

- **ユーザモードランナー** `rvemu run prog.elf` — Linux 用の静的 RV64 ELF を実行し、
  システムコール (write/read/exit…) はホスト側で処理する (qemu-user のようなもの)
- **システムモード** `rvemu boot kernel.elf` — QEMU `-machine virt -bios none` 相当の
  マシン (RAM・UART 16550・CLINT・電源断デバイス) でカーネルを M モードから起動する。
  `../os` の zigos がシェルまで起動する。

## 使い方

```sh
go build -o rvemu .

./rvemu run testdata/hello.elf            # Hello, RISC-V!
./rvemu run testdata/sum.elf; echo $?     # 5050 を表示, 終了コード 186 (=5050%256)
echo hello | ./rvemu run testdata/upcase.elf
./rvemu run --trace testdata/sum.elf      # 1 命令ずつトレース (stderr)
./rvemu run --stats prog.elf              # 実行命令数と MIPS を表示

# ../asm で作った ELF もそのまま動く
(cd ../asm && go run . build examples/fib.s -o /tmp/fib) && ./rvemu run /tmp/fib

# zigos を起動 (対話: 行単位入力。終了は halt)
(cd ../os && zig build)
./rvemu boot ../os/zig-out/bin/kernel.elf

# 非対話: コマンドを流し込む (zigos のシェルは \r でも \n でも 1 行とみなす)
printf 'ls\ncat /etc/motd\nps\nhalt\n' | ./rvemu boot --timeout 10s --stats ../os/zig-out/bin/kernel.elf
```

フラグ: `--trace` (命令トレース), `--stats` (命令カウンタ・速度), `--max-insts N`,
`--timeout 10s`, `--realtime` (boot: WFI で実時間だけ眠る), `-v` (未実装システムコールを報告)。

トレースの形式:
```
  命令番号 特権 PC              命令語     逆アセンブル
        10 M 0000000080000028:     02a1  addi t0, t0, 8       ← 16 ビット = 圧縮命令
        11 M 000000008000002a:     bfdd  jal zero, 0x80000020  (展開後の形で表示)
```

テスト: `go vet ./... && go test ./...`
(`../os/zig-out/bin/kernel.elf` があれば zigos の起動テスト・ブラウザ用 session のテストも走る。`RVEMU_KERNEL=` で場所を指定可)
`go test ./rv -run TestUser -update` で `testdata/*.elf` を作り直す。

## ファイル構成 (アーキテクチャ)

エミュレータ本体はインポート可能なパッケージ `rvemu/rv` (`rv/`) にあり、`main.go` は CLI だけ。

```
main.go          CLI (run / boot サブコマンド, フラグ)
cmd/wasm/        ブラウザ版 (web/os) の wasm エントリ。session.go は普通の go test で検証できる
rv/machine.go    Machine の組み立て: ユーザモード (メモリ配置・スタック) / システムモード (virt) / 実行ループ
rv/api.go        外部向け API: Snapshot, ReadMem, Disasm/DisasmAround, 命令トレースのリングバッファ
rv/cpu.go        CPU の状態, Step (fetch → decode → execute), 命令の意味 (execute), M/A 拡張, Zicsr, WFI
rv/decode.go     命令語 → Inst 構造体 (Op, rd, rs1, rs2, imm…)。ビットの切り出しはここだけ
rv/rvc.go        C 拡張: 16 ビット命令を対応する 32 ビット命令に展開
rv/encode.go     32 ビット命令のエンコーダ (RVC 展開とテスト用アセンブラで共用)
rv/csr.go        CSR の番号・読み書き (sstatus は mstatus の一部の「窓」、など)
rv/trap.go       例外・割り込みの受付 (trap), mret/sret, 委譲, 割り込み優先度, TrapEvent
rv/bus.go        物理アドレス → RAM / デバイスの振り分け, sifive_test (電源断) デバイス
rv/uart.go       UART 16550 (ポーリング用の最小実装。io.Reader から、または Push で入力)
rv/clint.go      CLINT (msip / mtimecmp / mtime)
rv/elf.go        ELF ローダ (ヘッダ解析は debug/elf, PT_LOAD のコピーは自前), シンボル表
rv/syscall.go    Linux システムコール (ユーザモードランナー用)
rv/disasm.go     逆アセンブラ
rv/*_test.go     命令クラスごとのテスト, テスト用ミニアセンブラ + ELF ビルダ (asm_test.go)
```

## Go から使う (パッケージ `rvemu/rv`)

他のモジュールからは `go.mod` に `require rvemu v0.0.0` と `replace rvemu => ../emu` を書いて
`import "rvemu/rv"` する。

```go
// ユーザモード: Linux 静的 ELF (例: ブラウザ内アセンブラの出力) を実行する
m, err := rv.NewUserMachine(elfBytes, []string{"prog", "arg1"}, &stdout, strings.NewReader("input"))
exited, code, err := m.Run(10_000_000)   // 最大 1000 万ステップ。exit すれば exited=true

// システムモード: QEMU virt 相当でカーネルを起動する
m, err := rv.NewVirtMachine(kernelBytes, &uartOut, nil, &rv.VirtOptions{RAMSize: 4 << 20})
m.SendInput([]byte("ls\r"))               // UART の受信データとして注入
```

| API | 説明 |
|---|---|
| `NewUserMachine(elf []byte, argv []string, stdout io.Writer, stdin io.Reader)` | argv は argv[0] 込み (空なら `a.out`)。stderr も stdout へ (`m.Proc.Stderr` で変更可)。stdin が nil なら EOF |
| `NewVirtMachine(kernel []byte, stdout io.Writer, stdin io.Reader, *VirtOptions)` | ELF または 0x80000000 に置く生バイナリ。stdin が nil なら入力は `SendInput` で |
| `m.Step()` | 1 ステップ = 1 命令、またはトラップの受付 1 回 |
| `m.Run(maxSteps) (exited bool, code int, err error)` | 0 なら止まるまで。停止・アイドル (下記)・`RunUntilTrap` 中のトラップで途中で戻る |
| `m.RunUntilTrap(maxSteps) (trapped bool)` | 次のトラップ (例外・割り込み・mret/sret・ユーザモードのシステムコール) まで |
| `m.Snapshot() Snapshot` | `X[32]`, `PC`, `Priv`, `Instret`, `CSR` (名前→値: mstatus mtvec mepc mcause mtval mie mip mscratch medeleg mideleg satp sepc scause stvec mhartid misa), `Mtime`, `Mtimecmp`, `Halted`, `ExitCode`, `Err` |
| `m.ReadMem(addr, n) []byte` / `m.WriteMem(addr, data)` | 物理アドレス。ReadMem はデバイスも読めるが副作用なし (UART の受信データを消費しない) |
| `m.Disasm(addr, count)` / `m.DisasmAround(pc, before, after)` | `[]DisasmLine{Addr, Size, Raw, Text, Symbol, OK}`。Around は 2/4 バイト命令混在でも pc に着地する開始位置を探す |
| `m.EnableTrace(n)` / `m.Trace() []TraceEntry` | 直近 n 命令のリングバッファ (`Seq, PC, Priv, Raw, Size, Text`)。文字列化は取り出すときだけ |
| `m.Traps() []TrapEvent` / `m.TrapCount()` / `m.OnTrap` | 直近 256 件のトラップ記録とフック。`TrapEvent{Seq, Kind, Cause, From, To, PC, Target, Tval, A7}`。Kind は `exception` `interrupt` `mret` `sret` `syscall` |
| `m.SetRealtimeIdle(true)` / `m.SetIdleBudget(ticks)` / `m.Idle()` | WFI の早送りを「与えた tick 数」までに制限する (ブラウザで実時間に合わせる)。使い切ると Run が戻り `Idle()` が true |
| `m.Symbols` / `m.Symbolize(addr)` | ELF のシンボル表。`"proc.schedule+0x1c"` の形にする |
| `rv.CauseName(cause)` / `rv.PrivName(p)` / `rv.RegNames` | mcause の名前, `"U"/"S"/"M"`, ABI レジスタ名 |
| `m.CPU`, `m.Bus`, `m.UART`, `m.CLINT`, `m.Proc` | 中身に直接触ることもできる (`m.CPU.Trace = w` で全命令トレースなど) |

ブラウザ版は `../web/os/` (`./build.sh` で wasm と kernel.elf を用意し、`python3 -m http.server -d ../web` で開く)。

1 命令の流れ (`CPU.Step`, `rv/cpu.go`):

```
 [CLINT tick] → mip.MTIP 更新 → 割り込み受付可能? ──yes──→ trap() して終わり
       │no
 fetch: PC から 16 ビット読む ── 下位 2 ビット != 11 → 圧縮命令: expandCompressed → 32 ビット命令
       │                     └─ == 11 → さらに 16 ビット読んで 32 ビット命令
 Decode(raw) → Inst{Op, Rd, Rs1, Rs2, Imm, ...}
 execute(Inst): レジスタ・メモリ・CSR を更新, nextPC を決める
       │例外 (*Exception) が返ったら trap()
 PC = nextPC, x0 = 0, Steps++
```

## 命令フォーマット

```
 31        25 24   20 19   15 14  12 11         7 6      0
| funct7     | rs2   | rs1   |funct3| rd          | opcode | R 形式 (add, mul…)
| imm[11:0]          | rs1   |funct3| rd          | opcode | I 形式 (addi, ld, jalr, csr…)
| imm[11:5]  | rs2   | rs1   |funct3| imm[4:0]    | opcode | S 形式 (sd…)
|imm[12|10:5]| rs2   | rs1   |funct3| imm[4:1|11] | opcode | B 形式 (beq…)
| imm[31:12]                        | rd          | opcode | U 形式 (lui, auipc)
| imm[20|10:1|11|19:12]             | rd          | opcode | J 形式 (jal)
```

即値はいつも符号拡張される (最上位ビットは必ず bit 31 にある → `int32(raw) >> n` で符号拡張できる)。
B/J 形式の即値がバラバラなのは、ハードウェアで rs1/rs2/rd の位置を固定するため。

圧縮命令 (C 拡張) は 16 ビットで、`rd'` のような 3 ビットのレジスタ欄は x8〜x15 を表す。
すべての圧縮命令は既存の 32 ビット命令の短縮形なので、`rvc.go` で展開してから
通常のデコーダに流している (`c.addi a0, a0, 1` → `addi a0, a0, 1`)。zigos の場合、
実行される命令の約半分が圧縮命令である。

## 特権モードとトラップ

特権レベルは M (3, マシン) / S (1, スーパーバイザ) / U (0, ユーザ)。
リセット直後は M モードで PC = ELF のエントリ (zigos では 0x80000000)。

例外 (ecall・不正命令・アクセス違反) や割り込み (タイマ) が起きると `trap()` が:

```
mepc   ← 例外を起こした命令の PC (割り込みなら次に実行するはずだった PC)
mcause ← 原因 (割り込みなら最上位ビット 1。例: タイマ = 0x8000000000000007, U からの ecall = 8)
mtval  ← 補足情報 (不正アドレス・不正命令の値)
mstatus.MPP ← 元の特権レベル,  mstatus.MPIE ← MIE,  MIE ← 0
特権レベル ← M,  PC ← mtvec (Vectored モードなら割り込みは mtvec + 4*原因番号)
```

`mret` はこの逆 (特権 ← MPP, MIE ← MPIE, PC ← mepc)。zigos はこれを利用して、
カーネル初期化後に `mstatus.MPP = U` にして `mret` することで U モードのシェルへ「戻る」。

割り込みが受け付けられる条件: `mip & mie` のビットが立っていて、かつ
現在 M 未満のモード、または M モードで `mstatus.MIE = 1`。

`medeleg` / `mideleg` のビットが立っている例外・割り込みは、U/S モードで起きた場合
S モードの CSR (sepc/scause/stvec…) に委譲され、`sret` で戻る (zigos は使わない)。

ユーザモードランナーでは、`ecall` は trap せずに直接 `syscall.go` が処理する
(カーネルがいないので)。それ以外の例外が起きたらエラーで停止する。

## メモリマップ

システムモード (QEMU virt と同じアドレス):

| アドレス      | デバイス                                                  |
|---------------|-----------------------------------------------------------|
| `0x0010_0000` | sifive_test: `0x5555` 書き込みで電源断, `0x3333\|code<<16` で失敗終了 |
| `0x0200_0000` | CLINT: msip `+0x0`, mtimecmp `+0x4000`, mtime `+0xbff8`  |
| `0x1000_0000` | UART 16550: THR/RBR `+0`, LSR `+5` (bit0 受信あり, bit5 送信可) |
| `0x8000_0000` | RAM 128 MiB                                               |

ユーザモード: RAM は 0 番地から。`[ELF][brk ヒープ 32MiB][mmap 領域][スタック 8MiB]`。
スタックには Linux と同じく `argc, argv…, NULL, envp NULL, auxv (AT_PHDR, AT_PAGESZ, AT_RANDOM…)` を積む。

対応システムコール: `read(63) write(64) writev(66) exit(93) exit_group(94) brk(214)
mmap(222, 匿名のみ) munmap mprotect ioctl(→ENOTTY) set_tid_address clock_gettime
rt_sigaction rt_sigprocmask getpid gettid`。
zig cc (`-target riscv64-linux-musl -mcpu=generic_rv64+m+a+c`) でビルドした
musl の C プログラム (printf, malloc, qsort) も動く。

## 時間について

`mtime` は **1 命令ごとに 1 増える** (実時間とは無関係)。そのため実行は決定的で、
同じ入力なら毎回同じ結果になる。`wfi` で待つときは次のタイマ割り込みの時刻まで
`mtime` を早送りする (アイドル時間はゼロで進む)。`--realtime` を付けると、早送りした分だけ
実際に眠るので、zigos の `uptime` などが実時間に近くなる。

## 実装していないもの (制限)

- F/D (浮動小数点) 拡張。musl などは `-mcpu=generic_rv64+m+a+c` でビルドする必要がある
- Sv39 などのページング (MMU)。`satp` は Bare モード以外の書き込みを無視する (WARL)
- PMP (書き込みは保存するだけで、アクセス制御はしない)
- PLIC と外部割り込み。UART はポーリングのみ (受信割り込みなし)
- 複数ハート, デバイスツリー (a1 = 0 で起動), OpenSBI / SBI 呼び出し
- 未知の CSR も「ただの記憶領域」として読み書きできてしまう (実機なら不正命令)
- ミスアラインのロード/ストアは例外にせずそのまま実行する (AMO は例外)
- 端末の raw モードは使わない (対話時は行単位で入力が届く)
- 命令キャッシュやデコードキャッシュは無いので遅い (30〜40 MIPS 程度)
