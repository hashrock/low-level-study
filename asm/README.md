# rvasm — 学習用 RISC-V アセンブラ + リンカ

GNU as 風の RISC-V (RV64IM) アセンブリを読み、**本物の ELF リロケータブル (.o)** を出力し、
それらを **静的 ELF64 実行ファイル** にリンクする小さなツールです。Go 標準ライブラリのみ。

```
go run . as    examples/hello.s -o hello.o        # アセンブル
go run . ld    main.o lib.o -o prog               # リンク
go run . build examples/main.s examples/lib.s -o prog   # 両方まとめて
cd ../emu && go run . run /abs/path/to/prog       # エミュレータで実行
```

出力ファイル名を省略すると、`as` は `foo.s → foo.o`、`ld`/`build` は `a.out` です。

## ファイル構成

| ファイル | 役割 |
|---|---|
| `lexer.go`  | 1 行をトークン (識別子・数値・文字列・記号) に分解。コメント `#` `//` |
| `parser.go` | ラベル / ニーモニック / オペランドに分割。レジスタ名・式・`off(reg)` の解釈 |
| `encode.go` | 命令表 (`opTable`) と R/I/S/B/U/J 形式のエンコード |
| `pseudo.go` | 擬似命令の展開 (`li` `la` `call` ...) と基本命令のオペランド読み取り |
| `asm.go`    | 2 パスアセンブラ本体・ディレクティブ処理 |
| `object.go` | ELF `.o` の書き出しと読み込み |
| `link.go`   | 静的リンカ (配置・シンボル解決・リロケーション適用) |
| `elf.go`    | ELF ファイル書き出しの共通部分、リロケーション種別定数 |
| `main.go`   | コマンドライン (`as` / `ld` / `build`) |
| `*_test.go` | エンコードの既知値テスト、ELF 構造チェック、テスト用ミニ CPU で examples を実行 |

## 対応している文法

```asm
    .equ  SYS_EXIT, 93          # 定数
    .data
msg: .asciz "hi\n"              # ラベル (複数可: a: b: nop)
    .equ  LEN, . - msg          # "." は現在位置。同じセクション内のラベル差は定数
    .text
    .globl _start
_start:
    la    a0, msg
    li    a7, SYS_EXIT
    ecall
```

- **レジスタ**: `x0`–`x31`、ABI 名 `zero ra sp gp tp t0–t6 s0–s11 a0–a7`、`fp` (= s0)
- **数値**: `123` `-5` `0x1f` `0b101` `0o17` `'A'` `'\n'`
- **式**: `+ - * << >> & | ~` と括弧。値は「シンボル + 定数」の形まで (`msg+4`)。
  ラベル同士の差 `a - b` は、両方が同じセクションで **それより前に** 定義されていれば定数
- **修飾子**: `%hi(x)` `%lo(x)` `%pcrel_hi(x)` `%pcrel_lo(label)`
- **ディレクティブ**:
  `.text .data .bss .rodata .section <name>` /
  `.globl .global .weak` / `.equ .set` / `.align n` `.p2align n` (2^n バイト) `.balign n` /
  `.byte .half .short .word .long .dword .quad` / `.ascii .asciz .string` / `.zero .space n[,fill]`。
  `.type .size .file .option .attribute` などは無視 (コンパイラ出力をそのまま通すため)
- **命令**: RV64I 全整数命令 (`lb`..`sd`, `add`..`sraw`, 分岐, `jal(r)`, `lui`, `auipc`,
  `ecall`, `ebreak`, `fence`) と M 拡張 (`mul`..`remuw`)。圧縮命令は出しません (全部 32bit)

## 命令形式とビット配置

RISC-V の 32bit 命令はすべて次の 6 形式のどれかです。レジスタ番号 (rd / rs1 / rs2) の位置が
形式間で共通なのがポイントで、エンコードは `固定ビット | rd<<7 | rs1<<15 | rs2<<20 | 即値ビット`
という 1 つの式で書けます (`encode.go` の `encode`)。形式ごとに違うのは即値の詰め方だけです。

```
 31        25 24    20 19    15 14  12 11         7 6       0
+------------+--------+--------+------+------------+---------+
|   funct7   |  rs2   |  rs1   |funct3|     rd     | opcode  |  R  add rd, rs1, rs2
+------------+--------+--------+------+------------+---------+
|      imm[11:0]      |  rs1   |funct3|     rd     | opcode  |  I  addi / ld / jalr
+------------+--------+--------+------+------------+---------+
| imm[11:5]  |  rs2   |  rs1   |funct3|  imm[4:0]  | opcode  |  S  sd rs2, imm(rs1)
+------------+--------+--------+------+------------+---------+
|[12]|[10:5] |  rs2   |  rs1   |funct3|[4:1] |[11] | opcode  |  B  beq rs1, rs2, label
+------------+--------+--------+------+------------+---------+
|              imm[31:12]             |     rd     | opcode  |  U  lui / auipc
+-------------------------------------+------------+---------+
|[20]|  imm[10:1]  |[11]| imm[19:12]  |     rd     | opcode  |  J  jal rd, label
+-------------------------------------+------------+---------+
```

- B / J の即値は常に偶数なので bit0 を持たず、その分だけ遠くへ飛べます (B: ±4KiB, J: ±1MiB)。
  ビットがバラバラに見えるのは、ハードウェアで「同じ即値ビットはなるべく同じ位置」にするため。
- RV64 のシフト即値 (`slli` 等) は I 形式の imm[5:0] が shamt、imm[11:6] が funct6
  (`srai` は 0b010000)。
- 命令表の 1 行は例えば `"sub": r(opOP, 0, 0x20)` (opcode=0x33, funct3=0, funct7=0x20)。

例: `addi sp, sp, -16` → imm=0xff0, rs1=2, funct3=0, rd=2, opcode=0x13 → `0xff010113`

## 擬似命令の展開

| 擬似命令 | 展開後 |
|---|---|
| `nop` | `addi x0, x0, 0` |
| `mv rd, rs` | `addi rd, rs, 0` |
| `not rd, rs` | `xori rd, rs, -1` |
| `neg rd, rs` / `negw` | `sub rd, x0, rs` / `subw` |
| `sext.w rd, rs` | `addiw rd, rs, 0` |
| `seqz rd, rs` | `sltiu rd, rs, 1` |
| `snez rd, rs` | `sltu rd, x0, rs` |
| `sltz` / `sgtz` | `slt rd, rs, x0` / `slt rd, x0, rs` |
| `beqz/bnez rs, L` | `beq/bne rs, x0, L` |
| `bltz/bgez rs, L` | `blt/bge rs, x0, L` |
| `bgtz/blez rs, L` | `blt/bge x0, rs, L` |
| `bgt/ble a, b, L` | `blt/bge b, a, L` (オペランドを入れ替え) |
| `bgtu/bleu a, b, L` | `bltu/bgeu b, a, L` |
| `j L` | `jal x0, L` |
| `jal L` | `jal ra, L` |
| `jr rs` | `jalr x0, 0(rs)` |
| `jalr rs` | `jalr ra, 0(rs)` |
| `ret` | `jalr x0, 0(ra)` |
| `call f` | `auipc ra, 0` + `jalr ra, 0(ra)`  ← `R_RISCV_CALL_PLT` |
| `tail f` | `auipc t1, 0` + `jalr x0, 0(t1)` ← `R_RISCV_CALL_PLT` |
| `la rd, sym` (`lla`) | `.Lpcrel_hiN: auipc rd, %pcrel_hi(sym)` + `addi rd, rd, %pcrel_lo(.Lpcrel_hiN)` |
| `li rd, imm` | 下記 |

### `li` (任意の 64bit 定数)

- 32bit に収まる値: `lui rd, hi20` + `addiw rd, rd, lo12` (不要な方は省略)。
  `addi` の即値は **符号拡張** されるので、`lo12` が負になる場合に備え `hi20 = (v + 0x800) >> 12` と丸めます。
  例: `0x12345fff` → `lui 0x12346` + `addiw -1`
- それより大きい値: 下位 12bit (符号付き) を引いた残りを再帰的に作り、`slli` で左へずらしてから
  `addi` で下位を足します (LLVM と同じ方法)。シフト量は残りの末尾ゼロを含めて最大化するので、
  `0x0000ffff00000000` は `lui`+`addiw`+`slli` の 3 命令で済みます。最悪 8 命令。
- `li` は命令数がパス 1 で決まる必要があるため、オペランドは **定数** (または事前に定義済みの `.equ`) に限ります。

## 2 パス方式

```
パス 1: 全行を走査し、命令/データの「サイズだけ」を数える → ラベルのオフセットが確定
パス 2: 同じ走査をもう一度。今度は実際にエンコードしてバイト列を作る
```

全命令が固定 4 バイト、擬似命令の展開数もオペランドだけで決まるので、パス 1 で位置が確定します。
パス 2 ではラベルが後方にあっても (前方参照) 値が分かります。

- **同じセクション内**の分岐/ジャンプ (`beq`, `j`, `jal`) → アセンブラがオフセットを直接計算
- それ以外 (別セクション・別ファイル・絶対アドレス) → **リロケーション** を記録してリンカに任せる

## オブジェクト形式: 本物の ELF64 リロケータブル

独自形式ではなく本物の ELF (`ET_REL`, `EM_RISCV`) を選びました。理由:

1. Go の `debug/elf` が読み書きの構造体 (`Header64`, `Section64`, `Sym64`, `Rela64`) を提供しており、
   書き出し側も 150 行ほどで済む (独自形式とあまり変わらない)
2. `readelf -a` / `objdump -dr` など既存ツールで中身を確認できる (学習時の答え合わせに便利)
3. リロケーション種別を psABI の `R_RISCV_*` にそのまま合わせられるので、知識がそのまま本物に通用する

`.o` の中身:

| セクション | 内容 |
|---|---|
| `.text` `.rodata` `.data` | 機械語・データ |
| `.bss` | `SHT_NOBITS` (サイズだけ持ち、ファイル上は 0 バイト) |
| `.rela.text` など | `Elf64_Rela {r_offset, r_info = sym<<32 \| type, r_addend}` の配列 |
| `.symtab` / `.strtab` | シンボル表 (ローカルが先、グローバルが後。`sh_info` = 最初のグローバル番号) |
| `.shstrtab` | セクション名 |

未定義のまま参照されたシンボル (`call puts` など) は `SHN_UNDEF` のグローバルとして出力されます。

## リロケーション

S = シンボルのアドレス、A = 加数 (addend)、P = 書き換える位置のアドレス。

| 種別 | 使われる場所 | リンカが書く値 |
|---|---|---|
| `R_RISCV_BRANCH` (16) | 別セクションへの `beq` 等 | B 形式即値 = S+A−P |
| `R_RISCV_JAL` (17) | 別セクションへの `jal` / `j` | J 形式即値 = S+A−P |
| `R_RISCV_CALL_PLT` (19) | `call` / `tail` | off=S+A−P を auipc (hi20) と次の jalr (lo12) に分配 |
| `R_RISCV_PCREL_HI20` (23) | `la` の auipc, `%pcrel_hi` | U 即値 = hi20(S+A−P) |
| `R_RISCV_PCREL_LO12_I/S` (24/25) | `la` の addi, `%pcrel_lo` | **対応する auipc の** オフセットの lo12 |
| `R_RISCV_HI20` (26) | `lui rd, %hi(sym)` | hi20(S+A) |
| `R_RISCV_LO12_I/S` (27/28) | `%lo(sym)` | lo12(S+A) |
| `R_RISCV_32` / `_64` (1/2) | `.word sym` / `.dword sym` | S+A そのもの |

### `%pcrel_lo` の仕組み (つまずきポイント)

```
.Lpcrel_hi1: auipc a0, %pcrel_hi(msg)      # P1 に置かれる
             addi  a0, a0, %pcrel_lo(.Lpcrel_hi1)
```

auipc は「自分の PC (P1) からの距離」の上位を足すので、addi の下位 12bit も **P1 からの距離**
`msg − P1` の下位でなければいけません (addi 自身の PC = P1+4 からの距離ではない)。
そこで ELF では `%pcrel_lo` のシンボルとして **auipc の位置を指すラベル** を書く決まりになっています。
リンカは先に全 `PCREL_HI20` の結果を「位置 → オフセット」の表に記録しておき、
`PCREL_LO12` を処理するときにその表を引きます (`link.go`)。

## リンカの処理

1. **配置**: 全 `.o` の同種セクション (`.text*` / `.rodata*` / `.data*` / `.bss*`) を
   アラインメントを守って入力順に連結
2. **アドレス決定**: 下記のメモリ配置に従い各出力セクションの仮想アドレスを決める
3. **シンボル解決**: グローバルシンボル表を作る (重複定義はエラー)。参照はまず
   そのファイル内のローカル、次にグローバル表を探す (見つからなければ undefined エラー)
4. **リロケーション適用**: 上の表に従い命令の即値部分を書き換え (`patchImm` はエンコーダの
   `immBits` を再利用) 、範囲外 (分岐 ±4KiB 超など) はエラー
5. **出力**: エントリポイント = グローバル `_start`

## 実行ファイルの ELF レイアウト

```
ファイルオフセット                 仮想アドレス
0x0000 ┌──────────────────────┐ 0x10000 ─┐
       │ ELF ヘッダ (64B)      │          │ PT_LOAD #1  R-X
       │ プログラムヘッダ×2     │          │ (ヘッダごとマップ)
0x00b0 │ .text                 │ 0x100b0  │
       │ .rodata               │          │
       ├──────────────────────┤        ─┘
       │ (ページ境界まで詰め物) │
0x1000 │ .data                 │ 0x11000 ─┐ PT_LOAD #2  RW-
       └──────────────────────┘          │ filesz = .data
         (.bss はファイルに無い)  .bss     │ memsz  = .data + .bss
                                         ─┘ (ローダが 0 で埋める)
       .symtab .strtab .shstrtab, セクションヘッダ表 (デバッグ用。ロードはされない)
```

- 2 つ目のセグメントは `vaddr ≡ offset (mod 0x1000)` を満たすよう `vaddr = 0x10000 + offset` にしています
  (mmap がページ単位でしかマップできないため)
- コード (R-X) とデータ (RW-) を別ページに分けることで、書き込み可能かつ実行可能なページを作りません
- `.data`/`.bss` が空なら 2 つ目のセグメントは出しません
- スタックはローダ (エミュレータ / カーネル) が `sp` に用意する前提です

## テストと検証

```
go vet ./... && go test ./...
```

- `TestEncodeKnown`: 仕様どおりの既知のエンコード値 (`addi sp,sp,-16 = 0xff010113` など 39 件) と比較
- `TestLiAllValues`: `li` の展開結果を解釈して元の値になるか (境界値・64bit 値)
- `TestExamples`: examples をビルド → `debug/elf` で構造を検査 (エントリ、W+X なし、
  オフセット/アドレスの合同) → テスト内のミニ RV64IM 解釈器で実行して出力を照合
- `TestCrossFileRelocs`: `%hi/%lo` (I/S)、`%pcrel_hi/%pcrel_lo`、`tail` を別ファイル間で検証
- 隣の `../emu` (`go run . run <prog>`) でも hello / main+lib の実行を確認

## examples

| ファイル | 内容 |
|---|---|
| `hello.s` | `write` + `exit` システムコール |
| `main.s` + `lib.s` | 別ファイルの `puts` / `print_u64` を `call`。`.rodata` / `.data` / `.bss` / `.dword sym` を使用 |
| `fib.s` | fib(0..50) をループで、fib(20) を再帰 (スタックフレーム) で計算して表示 |

## 制限

- RV64IM のみ (A/F/D/C 拡張なし)。圧縮命令は生成しない
- 数字ローカルラベル (`1:` / `1b` / `1f`) 未対応 → `.Lname:` を使う
- 1 行 1 文 (`;` 区切りは不可)
- `li` / `.equ` の値は使用箇所より前で確定している必要がある
- `lw a0, sym` のような「シンボル直接指定のロード/ストア」擬似命令は未対応 (`la` + `lw` を使う)
- リンカ緩和 (relaxation)・GP 相対・動的リンク・PIE・TLS・GOT は無し。`R_RISCV_RELAX` は読み込み時に無視
- `.section` のフラグ引数は無視し、名前の接頭辞で種類を決める
