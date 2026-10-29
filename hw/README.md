# hw — 自作 RISC-V CPU (Verilog)

`../emu` (Go のエミュレータ) の **ハードウェア版** です。同じ RV64IM の命令を、
Go の `switch` 文ではなく **配線とレジスタ** で実行します。
「命令のビットがどうやって電気信号になり、クロックの立ち上がりで結果がレジスタに入るのか」を
波形 (VCD) で 1 本ずつ追えるように、モジュールを小さく分けてあります。

- **単一サイクル RV64IM コア** (`rtl/core.v`): 1 クロックで 1 命令。CPI = 1
- **5 段パイプライン版** (`rtl/core_pipe.v`): IF/ID/EX/MEM/WB、フォワーディング、ハザード検出。
  デコーダ・ALU などの部品は単一サイクル版と共用
- **バス + RAM + MMIO コンソール**: valid/ready のハンドシェイク。RAM に待ちサイクルを入れてストールを見られる
- **lockstep 検証**: 同じ ELF を `../emu` で 1 命令ずつ実行し、全レジスタ・ストアを毎命令比較する
- `../asm` と `../cc` の ELF がそのまま動く (`printf` を使う C プログラムも)

```sh
brew install icarus-verilog                 # シミュレータ (Icarus Verilog 13.0 で確認)
cd hw
make run  PROG=../cc/examples/sieve.c       # C を自作 CPU で実行
make run  PROG="../asm/examples/main.s ../asm/examples/lib.s"
make wave PROG=examples/demo.s              # build/wave.vcd (波形) を作る
go run ./tools/vcdcat build/wave.vcd pc inst alu_y rf_we   # 波形を端末で表として読む
make lockstep PROG=../cc/examples/qsort.c   # emu と 1 命令ずつ突き合わせる
make test-short                             # 全テスト (長いもの以外, 十数秒)
make test                                   # fib.c (490 万命令) なども含めて全部 (数分)
```

必要なもの: Go 1.27、Icarus Verilog (`iverilog` / `vvp`)。GUI は不要です
(波形は GTKWave や Surfer で開けますが、付属の `tools/vcdcat` で端末でも読めます)。

---

## ファイル構成

| ファイル | 行数 | 役割 | 対応する emu のコード |
|---|---:|---|---|
| `rtl/defs.vh` | 62 | opcode・ALU 演算・マルチプレクサの選択値に名前を付ける | `rv/decode.go` の `Op` |
| `rtl/pc.v` | 19 | プログラムカウンタ (stall 中は保持) | `CPU.PC`, `Step` の `c.PC = c.nextPC` |
| `rtl/decoder.v` | 117 | 命令語 → 名前付きの制御信号 | `rv/decode.go` の `Decode` + `cpu.go` の `execute` の switch |
| `rtl/immgen.v` | 26 | I/S/B/U/J 形式の即値を組み立てて符号拡張 | `decode.go` の `immI` … `immJ` |
| `rtl/regfile.v` | 31 | x0..x31。読み 2 ポート (組み合わせ)・書き 1 ポート (クロック同期)、x0 = 0 | `CPU.X[32]`, `c.X[0] = 0` |
| `rtl/alu.v` | 43 | 加減算・論理・シフト・比較。`*W` 命令は 32 ビットで計算して符号拡張 | `execute` の ADD..AND, `sx32` |
| `rtl/muldiv.v` | 72 | M 拡張 (`*` `/` `%` で 1 サイクル)。0 除算・オーバーフローの規則 | `cpu.go` の `mulDiv` |
| `rtl/branch_unit.v` | 25 | 分岐条件 (eq / lt / ltu と反転) | `execute` の BEQ..BGEU |
| `rtl/lsu.v` | 50 | ロード/ストアユニット: バイトレーン配置・書き込みストローブ・符号/0 拡張 | `bus.go` の `Load`/`Store`, LB..SD |
| `rtl/core.v` | 171 | **単一サイクルコア**: 上の部品をつなぐデータパス | `cpu.go` の `Step` |
| `rtl/core_pipe.v` | 297 | **5 段パイプラインコア** (同じ部品 + パイプラインレジスタ・フォワーディング) | (emu にはタイミングの概念が無い) |
| `rtl/bus.v` | 46 | アドレスデコーダ: RAM / コンソール / どこでもない (bus_error) | `bus.go` の `Bus.Load`/`Store`/`Map` |
| `rtl/ram.v` | 70 | 1 MiB の統一メモリ (命令ポート + データポート)、待ちサイクル、`$readmemh` | `Bus.RAM []byte` |
| `rtl/console.v` | 33 | MMIO コンソール (UART 16550 の THR/LSR だけ) | `rv/uart.go` |
| `rtl/soc.v` | 78 | トップ: コア + バス + RAM + コンソール | `machine.go` の `Machine` |
| `tb/tb.v` | 197 | テストベンチ: クロック・リセット・**システムコール**・コミットトレース・VCD | `syscall.go`, `machine.go` の実行ループ |
| `tb/tb_pipe.v` | 3 | パイプライン版用 (`define PIPE` して `tb.v` を読むだけ) | |
| `loader/loader.go` | 168 | .s/.c → ELF (rvasm / rvcc) → RAM イメージ (hex) + 初期スタック | `elf.go` の `LoadELF`, `machine.go` の `setupStack` |
| `sim/sim.go` | 289 | iverilog のビルド、vvp の起動、**emu との lockstep 比較** | |
| `sim/sim_test.go` | 147 | 全プログラム × 2 コアの lockstep テスト | |
| `tools/load/` | 57 | `go run ./tools/load -o build/x prog.c` → `x.elf` `x.hex` `x.args` | |
| `tools/lockstep/` | 55 | 1 本だけ lockstep で実行する CLI | |
| `tools/vcdcat/` | 104 | VCD を「1 クロック 1 行」の表にして表示 (解析は `vcd/`) | |
| `vcd/` | 207 | VCD の読み込み (クロックの立ち上がり直前の値を 1 行にする) | |
| `tools/trace2json/` | 592 | `demos/*.s` を両方のコアでシミュレーションし、波形を `web/hw` 用の JSON にする | |
| `demos/*.s` | 161 | `web/hw` (ブラウザで 1 クロックずつ見るページ) のデモ: 演算・ロード/ストア・分岐・呼び出し・MMIO・ストール・load-use・フォワーディング・フラッシュ・ループ | |
| `tests/*.s` | 624 | 命令クラスごとの自己チェックテスト (alu, word, branch, loadstore, muldiv, fence, mmio, hazard) | `rv/*_test.go` |
| `examples/demo.s` | 22 | 波形の解説用 (addi・ロード・MMIO ストア・exit) | |
| **RTL 計 (rtl/)** | **1,140** | うち単一サイクル版に必要なのは core_pipe.v を除く 843 行 | |

---

## データパス (単一サイクル版)

```
        ┌──────────────── next_pc = (jump | 分岐成立) ? alu_y (jalr は bit0=0) : pc+4 ◀────────────┐
        ▼                                                                                        │
    ┌───────┐ pc   ┌──────────┐ inst  ┌─────────┐  制御信号: reg_we a_sel b_sel alu_op is_word     │
    │ pc_reg│─────▶│ RAM      │──┬───▶│ decoder │  is_muldiv imm_sel wb_sel mem_read mem_write     │
    └───────┘  │   │ 命令ポート│  │    └─────────┘  branch jump jalr ecall illegal  (→ 各 mux へ)  │
               │   └──────────┘  │     rs1 rs2 rd │                                               │
               │                 └──▶ immgen ─────┼─── imm ─────────────┐                         │
               │                                  ▼                     ▼                         │
               │                  ┌──────────────────┐ rs1_val ┌────────────┐ alu_a ┌─────┐ alu_y  │
               ├─────────────────▶│ regfile          │────┬───▶│ A: rs1/pc/0│──────▶│     │───┬────┘
               │                  │ 読み 2 / 書き 1  │    │    └────────────┘       │ ALU │   │
               │                  │                  │ rs2_val ┌────────────┐ alu_b │     │   │ d_addr
               │                  │                  │──┬─┼───▶│ B: rs2/imm │──────▶│     │   ├──────▶┌─────┐ d_valid/d_we
               │                  └────────▲─────────┘  │ │    └────────────┘       └─────┘   │       │ LSU │ d_wdata/d_wstrb
               │                           │            │ ├──▶ branch_unit ── br_taken ──────(next_pc)│     │───────▶┌─────┐──▶ RAM
               │                           │            │ └──▶ muldiv ──── md_y               │       │     │◀───────│ bus │──▶ console
               │                           │            └──────── rs2 (ストアする値) ──────────┼──────▶│     │ d_rdata│     │
               │                           │                                                  │       └─────┘ d_ready└─────┘
               │                           │ rf_wdata (クロックの立ち上がりで書く)               │     load_val │
               │                     ┌───────────┐                                            │          │
               └── pc+4 ────────────▶│ wb mux    │◀── ALU/muldiv の結果 (ex_y) ◀───────────────┘          │
                         sys_ret ───▶│ (wb_sel)  │◀── ロードした値 (load_val) ◀──────────────────────────┘
          (テストベンチ = OS から)    └───────────┘
```

**クロックの立ち上がりエッジ** で起きるのは 3 つだけです:
`pc ← next_pc`、`regs[rd] ← rf_wdata` (`rf_we` = 1 のとき)、`RAM[addr] ← d_wdata` (`d_valid & d_we & d_ready`)。
エッジとエッジの間は、`pc` から始まって全部の組み合わせ回路に値が流れていき、次のエッジまでに落ち着きます。
これが「1 サイクルで 1 命令」の意味です (落ち着くまでの最長経路 = クリティカルパスがクロック周期を決める)。

止まる (stall) のは、データバスが `d_ready = 0` を返したときと、`ecall` を OS (テストベンチ) が処理中のときだけです。
stall 中は `pc` も `rf_we` も止まり、同じ命令がもう 1 サイクル続きます。

### 制御信号の表

デコーダ (`rtl/decoder.v`) の出力と命令の種類の対応です。`-` は「何でもよい (使われない)」。

| 命令の種類 | 例 | `reg_we` | `a_sel` | `b_sel` | `imm_sel` | `alu_op` | `wb_sel` | `mem_read` | `mem_write` | `branch` | `jump` | その他 |
|---|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|---|
| R 形式 (OP) | `add` `sub` `sll` `slt` `xor` `sra` … | 1 | RS1 | RS2 | - | {bit30, funct3} | ALU | 0 | 0 | 0 | 0 | |
| R 形式 (M 拡張) | `mul` `mulh` `div` `remu` … | 1 | - | - | - | - | ALU | 0 | 0 | 0 | 0 | `is_muldiv=1` (funct7=1) |
| I 形式 (演算) | `addi` `andi` `slli` `srai` … | 1 | RS1 | IMM | I | {f3==101 & bit30, funct3} | ALU | 0 | 0 | 0 | 0 | |
| `*W` 命令 | `addw` `addiw` `sraw` `mulw` `divw` … | 1 | RS1 | RS2/IMM | I | 同上 | ALU | 0 | 0 | 0 | 0 | `is_word=1` |
| ロード | `lb` `lhu` `lw` `ld` … | 1 | RS1 | IMM | I | ADD | MEM | 1 | 0 | 0 | 0 | funct3 = サイズ・符号 |
| ストア | `sb` `sh` `sw` `sd` | 0 | RS1 | IMM | S | ADD | - | 0 | 1 | 0 | 0 | |
| 分岐 | `beq` `bne` `blt` `bgeu` … | 0 | PC | IMM | B | ADD | - | 0 | 0 | 1 | 0 | ALU は飛び先 pc+imm を計算 |
| `jal` | `jal ra, f` / `j L` | 1 | PC | IMM | J | ADD | PC4 | 0 | 0 | 0 | 1 | |
| `jalr` | `jalr` / `ret` | 1 | RS1 | IMM | I | ADD | PC4 | 0 | 0 | 0 | 1 | `jalr=1` (飛び先の bit0 を 0 に) |
| `lui` | `lui a0, 0x12345` | 1 | ZERO | IMM | U | ADD | ALU | 0 | 0 | 0 | 0 | 0 + imm |
| `auipc` | `auipc a0, 0` (`la` の前半) | 1 | PC | IMM | U | ADD | ALU | 0 | 0 | 0 | 0 | pc + imm |
| `ecall` | `ecall` | 1* | - | - | - | - | SYS | 0 | 0 | 0 | 0 | `ecall=1`。rd は a0 に差し替え。*exit のときは書かない |
| `fence` | `fence` | 0 | - | - | - | - | - | 0 | 0 | 0 | 0 | nop (単一サイクルでは順序が自明) |
| その他 | `ebreak`, CSR 命令, 未定義 | 0 | | | | | | | | | | `illegal=1` → テストベンチが停止 |

`alu_op` の値は `{bit30, funct3}` という 4 ビットで、`add`=0000、`sub`=1000、`srl`=0101、`sra`=1101 です
(`defs.vh`)。命令のビットがほぼそのまま ALU の制御線になるように RISC-V が設計されているのが分かります。
ただし `addi` の即値の bit30 は「sub」の意味ではないので、I 形式では `funct3 == 101` (右シフト) のときだけ bit30 を見ます。

---

## 波形を読む: `addi a0, a0, -1` (= `0xfff50513`)

```sh
make wave PROG=examples/demo.s LEN=20
go run ./tools/vcdcat build/wave.vcd pc inst reg_we a_sel b_sel imm_sel alu_op wb_sel \
       rs1 rs1_val imm alu_a alu_b alu_y rf_we rf_rd rf_wdata x_a0
```

`tools/vcdcat` は各クロックの **立ち上がりエッジ直前** の値 (= そのサイクルで落ち着いた値) を 1 行にします。
`examples/demo.s` の先頭 2 命令 `li a0, 5` → `addi a0, a0, -1` のところ (値は 16 進。読みやすいよう一部の列は先頭の 0 を省略):

```
pc     inst     reg_we a_sel b_sel imm_sel alu_op wb_sel rs1 rs1_val imm              alu_a alu_b            alu_y rf_we rf_rd rf_wdata x_a0
100b0  00500513 1      0     1     0       0      0      00  0       0000000000000005 0     0000000000000005 5     1     0a    5        0
100b4  fff50513 1      0     1     0       0      0      0a  5       ffffffffffffffff 5     ffffffffffffffff 4     1     0a    4        5   ← このサイクル
100b8  00001297 1      1     1     3       0      0      00  0       0000000000001000 100b8 0000000000001000 110b8 1     05    110b8    4   ← エッジの後
```

1. **命令バス**: `pc = 0x100b4` が RAM の命令ポートに入り、同じサイクル内に `inst = fff50513` が出てくる。
   ビットに分けると
   ```
   1111 1111 1111 | 01010 | 000 | 01010 | 0010011
     imm[11:0]=-1    rs1=10  f3=0  rd=10   opcode=OP-IMM (0x13)
   ```
2. **デコーダ** (`decoder.v`): opcode が `OP-IMM` なので `reg_we=1`、`a_sel=0` (RS1)、`b_sel=1` (IMM)、
   `imm_sel=0` (I 形式)、`alu_op=0` (ADD: funct3=000)、`wb_sel=0` (ALU)。`mem_read`/`mem_write`/`branch`/`jump` は 0。
3. **即値生成器** (`immgen.v`): `inst[31:20] = 0xfff` を符号拡張して `imm = 0xffff_ffff_ffff_ffff` (= -1)。
4. **レジスタファイルの読み出しポート**: `rs1 = inst[19:15] = 0x0a` (a0) を入れると、`rs1_val = 5` が
   (クロックを待たずに) 出てくる。
5. **ALU**: `alu_a = rs1_val = 5`、`alu_b = imm = -1`、`alu_op = ADD` → `alu_y = 4`
   (64 ビットの 5 + 0xffff…ffff は桁あふれして 4)。
6. **書き戻し**: `wb_sel = ALU` なので `rf_wdata = 4`、`rf_we = 1`、`rf_rd = 0x0a`。
   この行の時点では `x_a0` (レジスタの中身) はまだ **5**。次の立ち上がりエッジで書き込まれ、
   次の行 (`pc = 0x100b8`) では `x_a0 = 4` になっている。同じエッジで `pc` も `0x100b4 → 0x100b8` に進む。

GTKWave / Surfer で見るときは、`tb.dut.core` の下の `pc` `inst`、`tb.dut.core.u_dec` の制御信号、
`tb.dut.core` の `rs1_val` `alu_a` `alu_b` `alu_y` `rf_we` `rf_rd` `rf_wdata`、`tb` の `x_a0` を並べると上と同じ絵になります。
`tb.clk` の立ち上がりで `x_a0` が 5 → 4 に変わるのが「書き戻し」です。

### ロード: `ld a1, 0(t0)` (RAM からの読み出し)

```sh
go run ./tools/vcdcat build/wave.vcd pc inst mem_read d_valid d_we d_addr d_wstrb d_rdata d_ready \
       ram_valid con_valid load_val rf_rd rf_wdata
```
```
pc     inst     mem_read d_valid d_we d_addr d_wstrb d_rdata          d_ready ram_valid con_valid load_val
100c0  0002b583 1        1       0    11000  ff      1122334455667788 1       1         0         1122334455667788
```

- ALU が `t0 + 0 = 0x11000` を計算し、それがそのまま `d_addr` になる。`mem_read=1` なので `d_valid=1`、`d_we=0`。
- バス (`bus.v`) が `0x11000 < 1 MiB` を見て RAM を選ぶ (`ram_valid=1`, `con_valid=0`)。
- RAM は待ちサイクル 0 なので同じサイクルで `d_ready=1`、`d_rdata` = ワード `0x11000/8` の 64 ビット。
- LSU (`lsu.v`) がアドレスの下位 3 ビット (0) だけずらし、`ld` (funct3=011) なのでそのまま `load_val` にする。
  `lw` なら下位 32 ビットを符号拡張、`lbu` なら下位 8 ビットを 0 拡張、という具合。
- `wb_sel = MEM` なので `rf_wdata = load_val`、エッジで a1 に入る。

### MMIO ストア: `sb t2, 0(t1)` (t1 = 0x10000000, t2 = 'A')

```
pc     inst     mem_write d_valid d_we d_addr   d_wstrb d_wdata          d_ready ram_valid con_valid tx_valid tx_data
100cc  00730023 1         1       1    10000000 01      0000000000000041 1       0         1         0        00
100d0  00a00393 0         0       0    0000000a 04      0000000000040000 1       0         0         1        41   ← 'A'
```

- `sb` は「1 バイトだけ書く」ので LSU は `d_wstrb = 0000_0001` (バイト 0 だけ有効)、`d_wdata` の下位 8 ビットに `0x41`。
  (アドレスの下位 3 ビットが 2 なら `wstrb = 0000_0100`、データも 16 ビット左にずらして置く)
- バスはアドレスの上位ビット `0x100000` を見てコンソールを選ぶ (`con_valid = 1`)。CPU にとっては普通のストアと
  何も変わらず、**どこに届くかはバスのアドレスデコードだけで決まる**。これが MMIO。
- コンソールは待ちなしで `ready = 1` を返し、エッジで `tx_valid <= 1`, `tx_data <= 0x41` をラッチする。
  次のサイクルにテストベンチがそれを見て `A` を表示する (本物ならここにシリアル送信回路がつながる)。
- `examples/demo.s` は使っていないが、`tests/mmio.s` では送信前に `lbu t1, 5(t0)` で LSR (0x60: 送信可) を読む。
  読み出しも同じ経路で、コンソールが `rdata` のバイトレーン 5 に 0x60 を置いて返す。

### ストール: RAM に待ちサイクルを入れる

```sh
make wave PROG=examples/demo.s LEN=20 WAIT=2
go run ./tools/vcdcat build/wave.vcd pc inst d_valid d_we d_addr d_ready u_ram.cnt stall commit rf_we
```
```
pc     inst     d_valid d_we d_addr   d_ready cnt stall commit rf_we
100bc  f4828293 0       0    11000    0       0   0     1      1
100c0  0002b583 1       0    11000    0       0   1     0      0     ← ld: RAM が ready を返さない
100c0  0002b583 1       0    11000    0       1   1     0      0     ← まだ (同じ命令が続く)
100c0  0002b583 1       0    11000    1       2   0     1      1     ← 2 サイクル待って ready → 完了
100c4  10000337 0       0    10000000 1       0   0     1      1
100cc  00730023 1       1    10000000 1       0   0     1      0     ← コンソールへの sb は待ちなし
```

`ld` の間、`d_valid = 1` のまま `d_ready = 0` が 2 サイクル続き、その間 `stall = 1` で `pc` と `rf_we` が止まります。
RAM の中のカウンタ (`cnt`) が `wait_cycles` に達すると `d_ready = 1` になり、そのサイクルの終わりで転送が成立します。
このとき CPI は 1 より大きくなります (`demo.s` は 13 命令で 15 サイクル。`loadstore.s` を `WAIT=3` で動かすと 91 命令で 145 サイクル)。

---

## バスと MMIO の「応答」

データバスは **valid / ready** の 2 本の線で 1 回の転送を表します (AXI や TileLink の基本形と同じ考え方)。

```
            ┌── 1 ──┐ 2 ┌── 3 ──┐
 clk     ─┐ ┌─┐ ┌─┐ ┌─┐ ┌─┐ ┌─
          └─┘ └─┘ └─┘ └─┘ └─┘
 d_valid ___/‾‾‾‾‾‾‾‾‾‾‾‾‾\____    コア: アドレス・データを出して「お願い」
 d_ready ___________/‾‾‾‾\____    スレーブ: 「できた」(読み出しなら d_rdata が有効)
                         ↑ このエッジで転送成立 (書き込みが起き、コアは次の命令へ)
```

- **コア (マスタ)** は `d_valid=1` と `d_addr`・`d_we`・`d_wdata`・`d_wstrb` を出し、`d_ready=1` が来るまで出し続ける。
- **バス** はアドレスを見て 1 つのスレーブだけに `valid` を通し、そのスレーブの `ready`・`rdata` をコアに返す。
  どこにもつながっていない番地なら、すぐ `ready=1` を返して `bus_error` を立てる (テストベンチが停止して報告)。
- **RAM** は `wait_cycles` 回だけ `ready=0` を返してから `ready=1`。**コンソール** は常に `ready=1`。
- 書き込みは「`valid & ready` のサイクルの終わりのエッジ」で起きる。読み出しデータは `ready=1` のサイクル内に返る。

この CPU では RAM が同じサイクルで答えられる (待ち 0) ので、ハンドシェイクが無くても動きます。
それでも valid/ready にしておくと、遅いメモリやデバイスをつないだときに **コアは何も変えずに** 待てるようになります。

命令フェッチはバスを通らず、RAM の 2 つ目のポートに直結しています (常に待ちなし。命令キャッシュが常にヒットしている状態)。
命令とデータが同じ RAM にある (ストアした命令を実行できる) が、ポートは別、というのは
小さな CPU でよくある「ハーバード風」の構成です。

---

## システムコール: テストベンチが OS の代わりをする

`../asm` と `../cc` のプログラムは Linux のシステムコール (`a7` = 64 write / 93 exit / 214 brk) で出力・終了します。
このハードウェアには OS がいないので、次のようにしました:

1. コアは `ecall` を実行すると、外に **`ecall` 信号** を出し、`sys_done` が来るまでストールする (ハードウェア)
2. テストベンチは `ecall` を見ると、**階層参照** (`dut.core.u_rf.regs[17]` のように回路の中身を直接読む書き方) で
   `a0`〜`a2`, `a7` と RAM の中身を読み、write ならバイトを表示、exit なら終了、brk ならヒープの位置を計算する
3. 戻り値を `sys_ret` に置いて `sys_done = 1` (と `sys_we = 1`) にすると、次のエッジで普通の書き戻しと同じ経路
   (`wb_sel = SYS`) で `a0` に書かれる

**これはハードウェアではなくテストベンチの便宜です。** 本物の RISC-V では `ecall` は「トラップ」を起こし、
CPU が `mepc` に pc を保存して `mtvec` の番地 (カーネルのトラップハンドラ) に飛び、カーネルがソフトウェアで処理して
`mret` で戻ってきます。それをやるには CSR (Zicsr)、M モードのトラップ処理、`mret` が要ります。

この方法を選んだ理由:
- `../emu` のユーザモード (`rv/syscall.go`, qemu-user 相当) と **まったく同じ意味** になるので、lockstep で 1 命令ずつ比較できる
  (トラップハンドラを入れると、ハンドラの命令が emu の側には無いので比較がずれる)
- riscv-tests の `tohost` や Verilator のテストベンチでもよく使われる、検証の定番のやり方
- 「CPU がやること」と「OS がやること」の境界が `ecall` 信号 1 本として見える

本物のトラップ (Zicsr + `mtvec`/`mepc`/`mcause` + `mret`) は下の「次のステップ」に入れてあります。
入れれば、M モードの小さなハンドラ (アセンブリ) が MMIO コンソールに書く、という「全部ハードウェア上」の形にできます。

初期状態も OS (exec) の仕事なので、ローダ (`loader/loader.go`) が作ります: ELF の各セグメントと、
emu の `setupStack` と同じ形の初期スタック (`argc, argv…, NULL, envp NULL, auxv`) を RAM イメージ (hex) に焼き込み、
テストベンチがリセット直後に `pc = エントリ`、`sp = スタックの番地` を設定します。

---

## lockstep 検証 (emu と 1 命令ずつ比べる)

テストベンチは命令が完了する (`commit`) たびに 1 行を書きます:

```
C <pc>             <命令語> <rd> <書いた値>        <ストア番地>      <サイズ> <ストア値>
C 00000000000100b4 fff50513 10 0000000000000004 0000000000000000 0 0000000000000000
```

`sim/sim.go` はこの行をパイプで受け取りながら、同じ ELF を `rvemu/rv` (`NewUserMachine` → `Step`) で 1 命令ずつ実行し、
- pc と命令語が同じか
- トレースの書き込みを反映した「影のレジスタファイル」が emu の **x1〜x31 全部** と一致するか
- ストアの番地・サイズ・値が一致するか

を毎命令確かめます。emu 側は Verilog に合わせて、スタックを同じ番地 (RAM 1 MiB の末尾) に置き直し、
`0x10000000` に UART をつなぎます。最後に終了コードと出力 (emu の stdout と Verilog の stdout) も比べます。

食い違うと、最初に違った命令と直前のコミットを表示して止まります。ALU の `sra` をわざと壊したときの例:

```
lockstep: diverged at instruction #62: register a0 (x10): Verilog 0ffffffffffffff0, emu fffffffffffffff0
  直前のコミット (Verilog):
    C 00000000000101a0 f0000293 5 ffffffffffffff00 0000000000000000 0 0000000000000000
  → C 00000000000101a4 4042d513 10 0ffffffffffffff0 0000000000000000 0 0000000000000000
     000101a0: f0000293  addi t0, zero, -256
  => 000101a4: 4042d513  srai a0, t0, 4
     000101a8: ff000593  addi a1, zero, -16
```

パイプライン版を作っている途中でも、「RAM が遅いとき、ストール中にフォワーディングの供給元が WB を抜けて
古い sp を使ってしまう」というバグを `cc/structs` (WAIT=1) の 7 命令目で見つけてくれました
(`core_pipe.v` の ID/EX レジスタで、ストール中もフォワードされた値を保存し直しているのはそのため)。

---

## 動くもの

`make test` (= `go test ./...`) は以下を **単一サイクル版とパイプライン版の両方で** lockstep 実行し、
cc の例は `cc/examples/*.out` とも比較します。単一サイクル版はストールが無ければ CPI = 1 (サイクル数 = 命令数) です。

| プログラム | 出力 (抜粋) | 命令数 | 単一サイクル | パイプライン (CPI) |
|---|---|---:|---:|---:|
| `tests/alu` `word` `branch` `loadstore` `muldiv` `fence` | `alu ok` など (失敗ならケース番号で exit) | 118 / 63 / 78 / 91 / 105 / 29 | 同じ | 130 / 73 / 128 / 103 / 115 / 49 |
| `tests/mmio` | `mmio ok (UART THR)` (システムコールを使わず MMIO で出力) | 160 | 160 | 246 (1.54) |
| `tests/hazard` | `hazard ok` (フォワーディング・load-use・フラッシュ) | 65 | 65 | 88 (1.35) |
| `examples/demo` | `A` (MMIO) | 13 | 13 | 20 (1.54) |
| `asm/hello` | `Hello, RISC-V!` | 9 | 9 | 19 (2.11) |
| `asm/main+lib` | `main.s calls puts() in lib.s` / `7` `6` `1000000007` | 266 | 266 | 422 (1.59) |
| `asm/fib` | `0` `1` `1` … `12586269025` `6765` | 262,418 | 262,418 | 375,661 (1.43) |
| `cc/hello` | `Hello, RISC-V! 1 + 2 = 3` | 2,069 | 2,069 | 2,895 (1.40) |
| `cc/structs` | `struct A: size 12, i@4 d@8` … | 18,218 | 18,218 | 24,692 (1.36) |
| `cc/list` | `sum of odd squares = 165` | 15,052 | 15,052 | 20,724 (1.38) |
| `cc/strings` | `   42\|42   \|00042\|ff\|ok` | 19,871 | 19,871 | 27,242 (1.37) |
| `cc/argv` (引数 3 つ) | `sum of numeric args = 42` (exit 3) | 6,473 | 6,473 | 8,832 (1.36) |
| `cc/sieve` | `count up to 10000: 1229` / `largest: 9973` | 449,564 | 449,564 | 623,824 (1.39) |
| `cc/qsort` | `sum = 4682264` | 1,103,387 | 1,103,387 | 1,472,620 (1.33) |
| `cc/fib` | `fib(25) = 75025` | 4,937,776 | 4,937,776 | 6,666,334 (1.35) |
| `tests/loadstore_wait3` | (RAM 待ち 3) | 91 | 145 (1.59) | 157 (1.73) |
| `tests/mmio_wait2` | (RAM 待ち 2。コンソールは待ちなし) | 160 | 200 (1.25) | 286 (1.79) |
| `tests/hazard_wait2` | (RAM 待ち 2) | 65 | 77 (1.18) | 100 (1.54) |
| `cc/structs_wait1` | (RAM 待ち 1) | 18,218 | 26,500 (1.45) | 32,974 (1.81) |
| `cc/strings_wait2` | (RAM 待ち 2) | 19,871 | 38,071 (1.92) | 45,442 (2.29) |

(`_waitN` は RAM の待ちサイクルを N にしたもの。Icarus Verilog は 1 秒に 3〜5 万サイクル程度なので、
`cc/fib.c` (490 万命令) は 1 本で 2 分ほどかかります。`make test-short` / CI はこれと `qsort.c` を飛ばします)

---

## 5 段パイプライン版 (`rtl/core_pipe.v`)

```
          IF            ID                   EX                       MEM             WB
      ┌────────┐ fd_ ┌──────────────┐ de_ ┌──────────────────┐ em_ ┌────────────┐ mw_ ┌─────────┐
 pc ─▶│命令RAM │────▶│decoder immgen│────▶│ ALU muldiv branch │────▶│ LSU ─ bus  │────▶│ regfile │
      └────────┘     │regfile 読み  │     │ ↑フォワーディング │     └────────────┘     │ 書き    │
          ▲          │ハザード検出  │     └──────────────────┘          │             └─────────┘
          │          └──────────────┘        │ 分岐成立 (redirect)        │ em_result/mw_val
          └──────────────────────────────────┘ → IF/ID, ID/EX をフラッシュ │ (EX の入力へ戻す)
```

部品 (`decoder` `immgen` `regfile` `alu` `muldiv` `branch_unit` `lsu`) は単一サイクル版と同じものを使い、
ステージの間にパイプラインレジスタ (`fd_*` `de_*` `em_*` `mw_*`) を挟んでいます。ポートも同じなので、
`soc.v` は `` `ifdef PIPE `` でコアを差し替えるだけで、バス・RAM・テストベンチ・lockstep はそのまま使えます。

- **フォワーディング**: EX の入力 `ex_rs1`/`ex_rs2` は、1 つ前の命令 (EX/MEM の `em_result`) か
  2 つ前の命令 (MEM/WB の `mw_val`) が同じレジスタに書く予定ならその値を使う (`fwd_a`/`fwd_b` = 1 / 2)
- **load-use ストール**: EX にいるロードの結果を ID の命令がすぐ使うときだけ、IF/ID を 1 サイクル止めて
  EX にバブル (nop) を入れる (`load_use`, `id_stall`)
- **分岐**: 常に「分岐しない」と予測して pc+4 を読み続け、EX で成立が分かったら (`redirect`) 後ろの 2 命令を捨てる
- **ecall**: 先行命令が全部 WB を抜けるまで ID で待ち (`ecall_drain`)、EX でテストベンチに処理してもらう
- **バスの待ち**: MEM が `d_ready=0` の間、IF〜MEM を止めて WB にバブルを入れる

### CPI の比較

| | 単一サイクル | 5 段パイプライン |
|---|---:|---:|
| CPI (待ちなし, cc/sieve) | 1.000 | 1.388 |
| CPI (待ちなし, cc/qsort) | 1.000 | 1.335 |
| CPI (待ちなし, cc/fib) | 1.000 | 1.350 |
| CPI (RAM 待ち 2, cc/strings) | 1.916 | 2.287 |
| 1 サイクルにやること | 全部 (フェッチ〜書き戻し) | 1 ステージ分 |
| 同時に処理している命令 | 1 | 最大 5 |

パイプライン版の CPI は 1 より大きくなります (分岐成立・ジャンプで 2 バブル、load-use で 1 バブル、ecall で 3 バブル)。
**それでも速い** のは、1 サイクルの仕事が約 1/5 になるのでクロック周波数を上げられるからです
(単一サイクル版のクロック周期は「命令 RAM → デコーダ → レジスタ → ALU → データ RAM → 書き戻し」の全部の遅延で決まる)。
シミュレータ上の「サイクル数」だけを見ると単一サイクル版が勝って見えるので注意。
ここから先、CPI を 1 に近づけるのが分岐予測 (BTB)・分岐を ID で解決・ロード結果の MEM→EX 以外のフォワーディング、などです。

### 波形: バブルとフォワーディング

```sh
make wave CORE=pipe PROG=tests/hazard.s LEN=40
go run ./tools/vcdcat build/wave.vcd pc fd_inst de_inst em_inst mw_inst load_use id_stall fwd_a fwd_b redirect commit c_pc
```

`*_inst` は各ステージにいる命令 (バブルは `00000013` = nop)。`tests/hazard.s` のケース 2
(`sd t0,0(s0)` → `ld t1,0(s0)` → `addi t1,t1,2`) のところ:

```
pc     fd_inst  de_inst  em_inst  mw_inst  load_use id_stall fwd_a fwd_b redirect commit
100ec  00043303 00543023 02800293 00200f93 0        0        0     1     0        1   ← sd が EX: t0 を EX/MEM からフォワード (fwd_b=1)
100f0  00230313 00043303 00543023 02800293 1        1        0     0     0        1   ← ld が EX、addi が ID で t1 を使いたい → load-use
100f0  00230313 00000013 00043303 00543023 0        0        0     0     0        1   ← IF/ID は止まり、EX にバブル (nop)
100f4  02a00593 00230313 00000013 00043303 0        0        2     0     0        1   ← addi が EX: ld の値を MEM/WB からフォワード (fwd_a=2)
100f8  0cb31463 02a00593 00230313 00000013 0        0        0     0     0        0   ← バブルが WB に着いたサイクルは commit=0
```

分岐・ジャンプ (ケース 6 の `j .Lskip`) では `redirect=1` のサイクルの後、IF/ID と ID/EX が nop になります:

```
pc     fd_inst  de_inst  em_inst  mw_inst  redirect
1013c  00128293 00c0006f 00000293 00600f93 1         ← j が EX: 飛び先が分かった。ID の addi は間違い
10140  00000013 00000013 00c0006f 00000293 0         ← 後ろの 2 命令を捨てて (nop)、飛び先 0x10140 から読み直す
```

---

## emu の Go コードとの対応

| やること | emu (Go) | hw (Verilog) |
|---|---|---|
| 1 命令の実行 | `rv/cpu.go` の `Step`: fetch → `Decode` → `execute` → `c.PC = c.nextPC` を **順番に** 実行 | `core.v`: 全部が **同時に** 動く組み合わせ回路で、エッジで pc・レジスタ・メモリが一斉に更新 |
| 命令のフィールド | `rv/decode.go` の `Decode`: `r>>7&31` などでビットを切り出して `Inst{Op, Rd, Rs1, Rs2, Imm}` | `decoder.v`: `assign rd = inst[11:7]` — 切り出しは配線をつなぐだけ |
| 即値 | `decode.go` の `immI` … `immJ` (`int32(r)>>20` で符号拡張) | `immgen.v`: `{{52{inst[31]}}, inst[31:20]}` |
| 何をするか | `execute` の `switch in.Op { case ADD: … }` | `decoder.v` が制御信号を作り、`core.v` のマルチプレクサがデータの通り道を選ぶ |
| レジスタ | `CPU.X [32]uint64`、`Step` の最後に `c.X[0] = 0` | `regfile.v`: 読みは常時、書きはエッジ、x0 は読むと 0・書きは捨てる |
| ALU | `execute` の `x[in.Rd] = rs1 + imm` など、`sx32` | `alu.v` (`is_word` で 32 ビット演算 + 符号拡張) |
| 乗除算 | `cpu.go` の `mulDiv` (`bits.Mul64` と 0 除算の特別扱い) | `muldiv.v` (`*` `/` `%` + 同じ特別扱い) |
| 分岐 | `execute` の `case BEQ: if rs1 == rs2 { c.nextPC = … }` | `branch_unit.v` の `taken` と `core.v` の `next_pc` マルチプレクサ |
| メモリ | `rv/bus.go`: `Bus.Load`/`Store` が RAM か `Map` したデバイスかを探す。バイト単位のスライス | `bus.v` (アドレスデコード) + `ram.v` (64 ビット幅、`wstrb`) + `lsu.v` (レーン配置と拡張) |
| UART | `rv/uart.go` の `Store(off=0)` → `out.Write` | `console.v` の `tx_valid`/`tx_data` → テストベンチが表示 |
| システムコール | `rv/syscall.go` の `Process.Handle` (CPU から関数として呼ばれる) | `tb.v` の `always @(negedge clk)` (コアの `ecall` 信号に応答) |
| ELF の読み込み・スタック | `rv/elf.go` の `LoadELF`、`rv/machine.go` の `setupStack` | `loader/loader.go` (同じ形のスタックを hex に焼く) |
| 命令トレース | `--trace`、`EnableTrace` / `Trace` (逆アセンブル付き) | コミットトレース (`+trace=`)、VCD 波形 |

emu は「1 命令の意味」を順番に書いた **仕様書** で、Verilog は同じ意味を **並列に動く部品の配線** で表したものです。
lockstep 検証は「仕様書どおりに配線できているか」を全命令で確かめていることになります。

---

## 波形の見方

`make wave` が `build/wave.vcd` (Value Change Dump: 信号が変化した時刻と値のテキスト) を作ります。

```sh
make wave PROG=examples/demo.s                       # リセット直後から LEN=200 サイクル
make wave PROG=../asm/examples/fib.s SYM=fib_iter LEN=60   # pc が fib_iter に最初に来たところから
make wave PROG=tests/hazard.s CORE=pipe WAIT=2       # パイプライン版、RAM 待ち 2
```

記録範囲を過ぎるとシミュレーションも止まります (長いプログラムの最後まで VCD にすると巨大になるので)。
同時に `build/wave.trace` (その範囲のコミットトレース) もできるので、波形の pc と命令を対応づけるのに使えます。

- **端末で**: `go run ./tools/vcdcat build/wave.vcd 信号名...` (信号名は階層名の末尾でよい: `alu_y`, `u_dec.alu_op`)。
  `-pc 100b4 -before 1 -after 1` でその pc の前後だけ表示
- **Surfer (ブラウザ, インストール不要)**: https://app.surfer-project.org/ を開いて `build/wave.vcd` をドラッグ&ドロップ。
  左のツリーで `tb` → `dut` → `core` を開き、信号をダブルクリックで追加。値の表示形式は右クリック → Format → Hex
- **GTKWave**: (GUI アプリなので必要なら自分で `brew install --cask gtkwave`) `gtkwave build/wave.vcd`。
  SST ペインで `tb.dut.core` を選び、信号を Signals ペインへドラッグ

おすすめの信号セット: `tb.clk`、`tb.dut.core.pc`、`inst`、`u_dec.*` (制御信号)、`rs1_val`、`rs2_val`、`alu_a`、`alu_b`、
`alu_y`、`rf_we`、`rf_rd`、`rf_wdata`、`d_valid`、`d_ready`、`stall`、`tb.x_a0` (a0 の中身)。

---

## 制限

- **CSR・トラップ・割り込みが無い** (`csrr`/`csrw`/`mret`/`ebreak` は不正命令として停止)。M/S/U モードの区別も無い。
- **圧縮命令 (C 拡張) が無い**。`../asm` と `../cc` は 32 ビット命令しか出さないので困らないが、Zig で作った
  zigos は実行命令の約半分が圧縮命令
- A 拡張 (アトミック)、F/D (浮動小数点) も無い
- ミスアラインのアクセス (8 バイト境界をまたぐもの) はテストベンチが検出して停止する (emu は黙って実行する)。
  境界をまたがないミスアライン (`lw` を 0x...2 から、など) は動く
- 乗除算は `*` `/` `%` の 1 サイクル版。実機なら除算器はマルチサイクル (数十サイクルかけて 1 ビットずつ) にして、終わるまでストールする
- メモリは 1 MiB 固定 (`ram.v` の `WORDS` と `loader.RAMSize`)。ヒープ (brk) はスタックの 64 KiB 手前まで
- `read` システムコールは常に `-EBADF` (標準入力は無い)。対応するのは write / exit / exit_group / brk だけ (他は `-ENOSYS`)
- シミュレーション専用の書き方を含む (`$readmemh`、RAM の組み合わせ読み出し、`*` `/`)。FPGA に載せるには
  RAM を同期読み出しの BRAM にする (= 単一サイクルでは無理なのでパイプライン版を使う) などの変更が要る

### 次のステップ: zigos (`../os`) を起動するには

1. **Zicsr**: `csrrw`/`csrrs`/`csrrc` (+ 即値版) と CSR ファイル (`mstatus` `mtvec` `mepc` `mcause` `mtval` `mie` `mip`
   `mscratch` `satp` …)。デコーダに `OPC_SYSTEM` の funct3 ≠ 0 を足し、wb_sel に CSR の値を足す
2. **M モードのトラップ**: `ecall`・不正命令・アクセス違反で `mepc ← pc`, `mcause ← 原因`, `pc ← mtvec`, `mstatus` の
   MIE/MPIE/MPP を更新。`mret` でその逆。U モードとの切り替え (emu の `rv/trap.go` がそのまま仕様書になる)。
   ここまでで、`ecall` をテストベンチではなく M モードのハンドラ (アセンブリ) が MMIO コンソールに書く形にできる
3. **CLINT** (`mtime`/`mtimecmp`/`msip`) とタイマ割り込み (zigos のプリエンプティブスケジューラが使う)。
   割り込みは「命令の境目で pc を mtvec にすり替える」回路になる
4. **C 拡張**: フェッチを 16 ビット単位にし、`../emu/rv/rvc.go` と同じ展開器 (16 → 32 ビット) をデコーダの前に置く。
   または **zigos を C 拡張なしでビルドする** (`-mcpu=generic_rv64+m+a` 相当) 方が簡単
5. **A 拡張** (zigos のスピンロックなどが `amoswap`/`lr`/`sc` を使う場合) と、RAM を 0x80000000 に置く・UART の受信 (RBR/LSR bit0)

パイプライン版の改良 (分岐予測、ID での分岐解決、マルチサイクル除算器でのストール) も、lockstep テストがあるので安心して試せます。
