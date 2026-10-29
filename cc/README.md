# rvcc — C のサブセットを RISC-V 64 にする小さなコンパイラ

C のサブセット (`int` `char` `long` ポインタ 配列 `struct` …) を **RV64IM のアセンブリ** にします。
出力は隣の `../asm` でアセンブル・リンクし、`../emu` で Linux 静的 ELF として動きます。Go 標準ライブラリのみ。

隣の `../compiler` (動的型の JS 風言語 → RISC-V) との **対比** が目的です。
動的型の言語では「値がどの型か」を実行時に調べるため、生成コードの大部分がタグ検査・ランタイム呼び出しになり、
ランタイム (`runtime.s` 1,531 行) がコード生成 (`codegen.go` 642 行) より大きくなりました。
静的型の C なら型はコンパイル時に決まるので、`a + b` は `addw` 1 命令、`p->next` は `ld` 1 命令になり、
**ランタイムは crt0 (41 行のアセンブリ) と C で書いた小さな libc だけ** で済みます。

```sh
go run . emit  examples/fib.c                 # アセンブリを表示 (ソース行のコメント付き)
go run . emit  -O0 examples/fib.c             # 最適化なし (素朴なスタックマシン)
go run . build examples/list.c -o /tmp/list   # ELF を作る (複数の .c も可。../asm をライブラリとして使う)
go run . run   examples/argv.c hello world    # build して ../emu で実行 (後ろは argv)
go run . run   --stats bench/fib25.c          # 実行命令数を表示
go vet ./... && go test ./...                 # 例題・テストスイート・ホスト cc との照合
go test -v -run TestBench .                   # -O0/-O1/-O2 の命令数を表示
```

### ライブラリとして使う (`rvcc/rvcc`)

コンパイラ本体は `rvcc/rvcc` パッケージで、ファイルもサブプロセスも使いません
(`GOOS=js GOARCH=wasm` でもそのまま動きます)。アセンブル・リンクは `../asm` の `rvasm/rvasm` を
`go.mod` の `replace rvasm => ../asm` で直接 import しているので、C → ELF がすべてメモリ上で完結します。

```go
out, err := rvcc.Compile("fib.c", src, 2)   // *Output: Asm (アセンブリ) と Lines
                                            //   Lines[k] = アセンブリ k+1 行目を生んだ C の行 (0 は対応なし)
exe, err := rvcc.Build([]rvcc.Source{{Name: "fib.c", Text: src}}, 2)  // []byte: libc とリンクした ELF

outs, err := rvcc.CompileAll(srcs, 2)       // [crt0.s, libc.s, 各 .s] の *Output (途中経過を見せたいとき)
p, err := rvasm.BuildProgram(rvcc.AsmSources(outs)...)  // → リスティング・リロケーション付きでリンク
crt0, libcC, libcH := rvcc.Libc()           // 埋め込みの libc のソース
```

エラーは `*rvcc.Error` (`File` `Line` `Col` `Msg`、表示は `fib.c:3:12: error: ...`) で返ります。
`run` サブコマンドだけは今も `../emu` を `go run` で起動します。

## ファイル構成

`main.go` (CLI) 以外のコンパイラは `rvcc/` パッケージに、libc は `rvcc/libc/` にあります。

| ファイル | 行数 | 役割 |
|---|---:|---|
| `api.go` | | 公開 API (`Compile` `CompileAll` `Build` `Libc`)、アセンブリ行 → C の行の対応 |
| `lexer.go` | 224 | 字句解析。`#` で始まる行は読み飛ばす、隣接する文字列の連結、`__LINE__` |
| `ast.go` | 184 | 型 (`Type`)、変数・関数 (`Obj`)、構文木 (`Node`)、struct のレイアウト計算 |
| `parser.go` | 884 | 再帰下降パーサ。スコープ・typedef・enum・struct タグの解決、初期化子の展開 |
| `check.go` | 456 | **型付けパス**: 式に型を付け、ポインタ演算の拡大・暗黙の型変換を木に書き込む。定数式の計算 |
| `codegen.go` | 797 | 型付きの木 → アセンブリ。フレーム配置、式スタック、-O0/-O1/-O2 |
| `main.go` | | CLI (`emit` / `build` / `run`)、`../emu` の起動 |
| **コンパイラ計** | **2,728** | (うちコメント・空行を除くと約 2,450) |
| `libc/crt0.s` | 41 | `_start` と システムコール 4 つ (`exit` `write` `read` `__brk`) |
| `libc/libc.c` | 222 | `printf` `puts` `putchar` `malloc` `strlen` `strcmp` `memcpy` `memset` … **rvcc 自身でコンパイル** |
| `libc/libc.h` | 30 | 全 .c の先頭に暗黙に読み込まれる宣言 |
| `rvcc_test.go` | 226 | テスト (下記「検証」) |
| `tests/test.c` | 490 | 機能ごとの `assert_eq` テスト (chibicc 風) |
| `examples/*.c` | | hello, fib, sieve, list, strings, structs, argv, qsort と期待出力 `*.out` |
| `bench/` | | `fib25.c` と、`../compiler` 用の同じプログラム `fib25.lang` |

`rvcc/libc/` に置いているのは、Go のツールがパッケージ直下の `.s` を Go アセンブラのファイルとみなすためです
(`../compiler` の `runtime/` と同じ理由)。libc は `go:embed` でバイナリに埋め込みます。

## 処理の流れ

```
 .c ──▶ lexer ──▶ parser ──────────────▶ check ──────────────▶ codegen ──▶ .s ─┐
        トークン   名前を解決した構文木      型付きの木                          ├─▶ ../asm ─▶ ELF ─▶ ../emu
 libc.c ─(同じ流れ)──────────────────────────────────────────────────────▶ .s ─┤
 crt0.s ──────────────────────────────────────────────────────────────────────┘
```

- **parser**: C は「使う前に宣言する」言語なので、読みながらスコープを積めば名前解決は一度で済みます。
  `T x;` と `a * b;` を区別するために typedef 名を知っている必要があるのも、パーサが名前を持つ理由です。
- **check**: 式に型を付け、コード生成が型を見て命令を選ぶだけで済むよう木を書き換えます。
  - `p + i` → `p + i*sizeof(*p)`、`p - q` → `(p - q) / sizeof(*p)`、`a[i]` は parser の段階で `*(a + i)`
  - 代入・引数・`return` で狭い型へ入れるときは `NCast` を挟む
  - `sizeof expr` を数値に、`s.x` をメンバ (オフセット) に解決
  - 型エラー (`no member named b`、`cannot dereference int`、引数の個数違い…) を報告
- **codegen**: 型付きの木を 1 回なめてアセンブリを出します。

## 対応している C のサブセット

**型**

| 型 | サイズ | アラインメント | 備考 |
|---|---:|---:|---|
| `char` | 1 | 1 | 符号付き (`lb` で符号拡張して読む) |
| `short` | 2 | 2 | |
| `int` | 4 | 4 | 演算は `addw` `mulw` など 32bit 命令で、結果は 32bit で回り込む |
| `long` (`long long`) | 8 | 8 | |
| ポインタ | 8 | 8 | `void *` も可 (算術は 1 バイト単位) |
| `T[n]`, `T[n][m]` | n × sizeof(T) | T と同じ | 多次元可。`[]` は初期化子から長さを決める |
| `struct` | 下記 | メンバの最大 | 入れ子、自己参照 (`struct node *next`)、無名 struct |
| 関数ポインタ | 8 | 8 | `int (*f)(int, int)`、`typedef int (*binop)(int, int)` |
| `enum` | 4 | 4 | `int` と同じ扱い。値の指定 (`GREEN = 5`) 可 |

`int` は 32bit、`long` は 64bit (LP64。macOS や Linux の cc と同じ) にしたので、
ホストの cc とサイズ・オーバーフローの挙動が一致し、出力をそのまま照合できます。
レジスタの中の整数は常に「型の幅で符号拡張された 64bit 値」にしておく、という約束で 32bit 演算を実現しています
(`int` の結果は `w` 付き命令が自動で符号拡張し、`char` への変換だけ `slli`+`srai` が要る)。

**struct のレイアウト規則** (`ast.go` の `layoutStruct`、RISC-V psABI = 普通の C と同じ)

1. 各メンバは、自分のアラインメントの倍数のオフセットに置く (間は詰め物)
2. struct のアラインメントはメンバの最大アラインメント
3. struct のサイズはそのアラインメントの倍数に切り上げる (配列にしたとき次の要素も揃うように)

```
struct A { char c; int i; char d; };     sizeof = 12
 0   1   2   3   4   5   6   7   8   9  10  11
+---+---+---+---+---+---+---+---+---+---+---+---+
| c | pad       | i             | d | pad       |
+---+---+---+---+---+---+---+---+---+---+---+---+
```

`examples/structs.c` がサイズとオフセットを表示し、ホストの clang と同じ値になることを確かめています。

**式**: C の優先順位どおり全部。`+ - * / %`、`== != < <= > >=`、`&& || !` (短絡評価)、`& | ^ ~ << >>`、
`=` と複合代入 `+= -= *= /= %= <<= >>= &= |= ^=`、前置・後置の `++ --`、単項 `& *`、`.` `->`、
ポインタ演算 (要素サイズで拡大)、`a[i]` (= `*(a+i)`、`2[a]` も可)、関数呼び出し、関数ポインタ経由の呼び出し、
`?:`、`,`、キャスト `(T)e`、`sizeof` (型・式)、文字・文字列リテラル (エスケープ、隣接連結)、`123L`

**文**: `if/else`、`while`、`for` (初期化部で宣言可)、`do-while`、`switch/case/default` (フォールスルー)、
`break` `continue`、`return`、ブロックスコープ、初期化子付きのローカル変数宣言 (`int a[3] = {1, 2}`、
`char s[] = "hi"`、`struct P p = {1, {2, 3}}`、`struct P q = p`)、ローカルの `typedef`

**関数・グローバル**: 定義とプロトタイプ、引数は 8 個まで (`a0`–`a7`)、再帰・相互再帰、
外部の関数呼び出し (別の .c や手書きの .s)、可変長引数の関数定義 (`...` と `__va_start()`, 下記)、
初期化子付きのグローバル変数 (整数、文字列、配列、struct、他のグローバルのアドレス `&g.y`、関数のアドレス)、
`static` (シンボルをファイル内に閉じる)、`extern`

**プリプロセッサは無し**: `#` で始まる行は無視します。その代わり `libc/libc.h` (プロトタイプと
`NULL` `EOF` の enum、`size_t`) を全ファイルの先頭に暗黙に読み込むので、
`#include <stdio.h>` と書いたプログラムがホストの cc と rvcc の両方でそのまま通ります。

## 呼び出し規約とスタックフレーム

RISC-V psABI (LP64) の整数規約にそのまま従います。なので rvcc の関数から手書きのアセンブリ (`crt0.s`) を呼べるし、
アセンブリから rvcc の関数 (`_start` → `main`) も呼べます。

| レジスタ | 役割 | 保存するのは |
|---|---|---|
| `a0`–`a7` | 引数 1〜8、`a0` は戻り値。rvcc では `a0` が式の値 (アキュムレータ) | 呼び出し側 |
| `ra` | 戻りアドレス | 呼ばれた側が prologue で退避 |
| `s0` = `fp` | フレームポインタ | 呼ばれた側 |
| `s1`–`s11` | -O2 の式スタック (使った分だけ保存) | 呼ばれた側 |
| `t0`–`t2` | 一時 (降ろした値・即値・アドレス) | 壊してよい |
| `sp` | スタックポインタ。呼び出し時は 16 バイト境界 | |

`char`/`short`/`int` の引数・戻り値は 64bit に符号拡張して渡します (psABI の規定。レジスタ上の約束と同じなので命令は要らない)。

```
        高位アドレス
        |  呼び出し元のフレーム    |
fp ───▶ +-------------------------+  ← 呼び出し時の sp (16 バイト境界)
 fp-8   | ra (戻りアドレス)         |
 fp-16  | 呼び出し元の fp           |
 fp-20  | 引数 n  (int: 4 バイト)   |  引数は prologue で a0.. からここへ保存
  ...   | ローカル変数              |  型のアラインメントに合わせて配置 (配列・struct は 8)
        | 可変長引数の退避場所 a0..a7 |  (可変長関数だけ)
 sp+8k  | 保存した s1..sk           |  -O2 で使った s レジスタ
sp ───▶ +-------------------------+  フレームサイズは 16 の倍数
  (-O0 では式の途中結果をこの下に push / pop する)
        低位アドレス
```

フレームの大きさは本体を生成し終えるまで (どの s レジスタを使ったか分かるまで) 決まらないので、
本体を別のバッファに出してから prologue を前に付けます。2 KiB を超えるフレーム
(`char big[5000]` など、`addi` の 12bit 即値に収まらない) では `li` + `sub` で確保します。

### 可変長引数と printf

`printf` は libc.c の中で C で書いています。RISC-V では可変長引数も普通の引数と同じく `a0`–`a7` で渡るので、
`...` を持つ関数は prologue で `a0`–`a7` をフレームに **連続して** 保存するだけで、引数が配列のように並びます。
組み込みの `__va_start()` がその「最初の可変長引数」のアドレスを返します。

```c
int printf(char *fmt, ...) {
  long *ap = __va_start();   // a0=fmt, a1.. が ap[0], ap[1] ...
  ...  v = *ap++;
```

`printf` は `%d %i %u %ld %x %lx %p %c %s %%`、幅 (`%5d`)、0 埋め (`%08x`)、左寄せ (`%-5s`) に対応します。
引数は合わせて 8 個まで (スタック渡しの引数は未対応)。

## サンプル: fib がどうなるか

<table>
<tr><th>ソース</th><th>-O2 (既定)</th><th>-O0 (素朴なスタックマシン)</th></tr>
<tr><td>

```c
int fib(int n) {
  if (n < 2)
    return n;
  return fib(n - 1)
       + fib(n - 2);
}
```

</td><td>

```asm
fib:
  addi sp, sp, -32   # prologue
  sd ra, 24(sp)
  sd fp, 16(sp)
  addi fp, sp, 32
  sd s1, 0(sp)       # callee-saved
  sw a0, -20(fp)     # 引数 n
  # 5: if (n < 2) return n;
  lw a0, -20(fp)     # n
  li t1, 2
  bge a0, t1, .Lelse1
  lw a0, -20(fp)     # n
  j .Lret_fib
.Lelse1:
  # 6: return fib(n - 1) + fib(n - 2);
  lw a0, -20(fp)     # n
  addiw a0, a0, -1
  call fib
  mv s1, a0          # 左辺を s1 に置く
  lw a0, -20(fp)     # n
  addiw a0, a0, -2
  call fib           # s1 は壊されない
  addw a0, s1, a0
.Lret_fib:
  ld s1, 0(sp)
  ld ra, 24(sp)      # epilogue
  ld fp, 16(sp)
  addi sp, sp, 32
  ret
```

</td><td>

```asm
  # 6: return fib(n - 1) + fib(n - 2);
  addi a0, fp, -20   # &n
  lw a0, 0(a0)
  addi sp, sp, -8    # push n
  sd a0, 0(sp)
  li a0, 1
  ld t1, 0(sp)       # pop
  addi sp, sp, 8
  subw a0, t1, a0
  addi sp, sp, -8    # push 引数
  sd a0, 0(sp)
  ld a0, 0(sp)       # pop → a0
  addi sp, sp, 8
  call fib
  addi sp, sp, -8    # push 結果
  sd a0, 0(sp)
  ...                # fib(n - 2) も同じ
  addi sp, sp, -8    # sp を 16 境界に
  call fib
  addi sp, sp, 8
  ld t1, 0(sp)       # pop 左辺
  addi sp, sp, 8
  addw a0, t1, a0
```

</td></tr>
</table>

`int` なので `addiw` / `addw` (32bit 演算) が選ばれています。タグもランタイム呼び出しも無く、
C のソースとアセンブリがほぼ 1 対 1 で対応します。

## 最適化: スタックマシン → レジスタ

`-O0` は教科書どおりのスタックマシンです。式の値は常に `a0`、二項演算の左辺はメモリに push しておき、
右辺を計算したら pop して演算します。変数は毎回「アドレスを計算 → `lw`」の 2 命令。

- **-O1**: 「葉」(定数・ローカル変数・グローバル配列のアドレス) は push せず直接レジスタへ。即値命令 (`addiw a0, a0, -1`)、
  ローカル変数は `lw a0, -20(fp)` の 1 命令、比較と分岐を 1 命令に (`slt`+`beqz` → `bge`)、定数畳み込み、
  引数の最後の非葉はそのまま `a`レジスタへ。
- **-O2** (既定): 残った式スタックを **callee-saved レジスタ `s1`–`s11`** に置く。push / pop が `mv` 1 命令 (または 0 命令) になる。
  s レジスタは呼ばれた側が保存する約束なので、`fib(n-1) + fib(n-2)` の左辺を `s1` に置いたまま `fib` を呼べます
  (t レジスタだと呼び出しで壊れる)。深さ 12 以上はメモリに溢れさせます。

`go test -v -run TestBench .` の結果 (`../emu --stats` の実行命令数。libc と `_start` の分も含む):

| プログラム | -O0 | -O1 | -O2 | -O0 → -O2 |
|---|---:|---:|---:|---:|
| `bench/fib25.c` (fib(25) を表示) | 8,984,249 | 4,977,801 | 4,856,316 | **−46%** |
| `examples/sieve.c` (10000 までの篩) | 1,100,562 | 502,108 | 449,564 | **−59%** |
| `examples/qsort.c` (1000 要素, 関数ポインタで比較) | 2,441,578 | 1,287,089 | 1,103,387 | **−55%** |

fib(25) は 242,785 回の呼び出しなので、1 回あたり -O0 で約 37 命令、-O2 で約 20 命令です。
-O2 の上積みが fib で小さいのは、-O1 の時点で「葉は積まない」によって push がほとんど消えているためで、
式の入れ子が深い sieve / qsort (`a[i] = ...`、`cmp(a[i], pivot)`) では s レジスタの効果がよく見えます。

## ../compiler (動的型) との比較

| | ../compiler (minilang, 動的型) | rvcc (C, 静的型) |
|---|---|---|
| コード生成 | `codegen.go` 642 行 (+ `resolve.go` 282) | `codegen.go` 797 行 (+ 型付け `check.go` 456) |
| ランタイム | `runtime.s` **1,531 行** (アセンブリ) | `crt0.s` **41 行** + `libc.c` 222 行 (C、rvcc でコンパイル) |
| fib(25) だけのプログラム (`bench/`) | **16,752,333 命令** | **4,856,316 命令** (-O2) / 8,984,249 (-O0) |
| fib の例題全体 (fib(0..14), fibLoop(50), fib(25)) | 16,985,126 命令 | 5,060,768 命令 (-O2) |
| fib 1 回の呼び出し | 約 69 命令 | 約 20 命令 (-O2) |
| fib 関数の静的な命令数 (ラベル・コメントを除く) | 89 命令 | 24 命令 (-O2) |

(どちらも `../emu run --stats` で計測。`bench/fib25.lang` を `../compiler` でビルドして同じ条件で比べた)

**型が静的だと消えるもの**

- **タグ検査**: `n - 1` のたびに「両方整数か?」(`and` `andi` `beqz`) と、違ったときの `rt_sub` への分岐。
  C では型がコンパイル時に分かるので `addiw a0, a0, -1` 1 命令。
- **タグの付け外し (boxing)**: 整数 n を `2n+1` で表すための `addi a0, a0, ±1`。真偽値も `FALSE(4)`/`TRUE(6)` に変換してから
  `if` で `FALSE` と `NIL` の 2 回比較していた → C では比較と分岐が `bge` 1 命令。
- **実行時ディスパッチ**: 関数はクロージャ値なので、呼ぶたびに `la g_fib; ld` で値を取り出し、`rt_checkcall`
  (関数か? 引数の個数は合うか?) を呼んでから `jalr`。C では `call fib` 1 命令で、個数の検査はコンパイル時に済んでいる。
- **ランタイムの大部分**: 文字列連結・比較、配列・マップ、型エラーのメッセージ、実行時エラーの位置表、
  スタックあふれの検査、GC 代わりのヒープ。C ではそれらは言語の外 (ライブラリかプログラマの責任) になり、
  必要な libc は C 自身で書ける。`printf` すら普通の C 関数です。

逆に C で増えたものは **型付けパス** (`check.go`): ポインタ演算の拡大、struct のオフセット、整数の幅の変換を
コンパイル時に決めるための 456 行です。ランタイムがやっていた仕事の一部がコンパイラに移った、と見ることができます。

## ランタイム (crt0 + libc)

- `crt0.s`: `_start` がスタックの先頭から `argc` (`ld a0, 0(sp)`) と `argv` (`addi a1, sp, 8`) を取り出して `main` を呼び、
  戻り値で `exit`。システムコールは `li a7, 番号` + `ecall` だけ (`exit`=93, `write`=64, `read`=63, `brk`=214)。
- `libc.c` (rvcc でコンパイル): `putchar` `puts` `printf` (1 回分をバッファにためて `write` 1 回)、
  `malloc` (brk を 64 KiB 単位で伸ばすバンプアロケータ、`free` は何もしない)、`calloc`、
  `strlen` `strcmp` `strncmp` `strcpy` `strcat` `memcpy` `memset` `memcmp` `atoi` `abs`、テスト用の `assert_eq`。

## 検証

```
$ go vet ./... && go test -count=1 ./...
ok  	rvcc	7.1s
```

- `TestExamples`: `examples/*.c` を -O0 / -O1 / -O2 それぞれでコンパイル → `../asm` → `../emu` 実行し、`*.out` と照合 (8 本 × 3)。
  `argv.c` は先頭の `// args:` 行を引数として渡す
- `TestSuite`: `tests/test.c` (機能ごとに 18 関数、216 個の `assert_eq(期待値, 式, __LINE__)`) を 3 つの最適化レベルで実行
- `TestHostCC`: **同じソースをホストの cc (macOS では Apple clang) でコンパイルして実行し、`*.out` と一致するか**。
  `*.out` は clang の出力から作ったので、rvcc の出力が正しいことの「答え合わせ」になっています。
  `tests/test.c` も `tests/host_shim.c` と一緒に clang で通します (cc が無ければ skip)
- `TestMultiFile`: 2 つの .c を別々にコンパイルしてリンク (`extern` 変数、`static` 関数)
- `TestErrors`: 未宣言の名前、存在しないメンバ、引数の個数、void の return、`unsigned` など 12 件のコンパイルエラー
- `TestBench`: 上の命令数を表示し、最適化で出力が変わらないこと・命令数が減ることを確認
- `../asm` / `../emu` をビルドできない環境ではテストを skip します

| example | 内容 |
|---|---|
| `hello` | `printf` と `puts` |
| `fib` | 再帰とループ (`../compiler/examples/fib.lang` と同じ内容) |
| `sieve` | エラトステネスの篩 (グローバル配列、入れ子のループ、`continue`) |
| `list` | `struct` + `malloc` の連結リスト、`typedef`、反転、`Node **` での削除 |
| `strings` | 文字列関数、ポインタの差、ポインタ演算の拡大、`2[p]`、文字列の配列、printf の書式 |
| `structs` | `sizeof` とオフセット (詰め物)、入れ子の struct、struct のコピー |
| `argv` | コマンドライン引数、`char **` |
| `qsort` | クイックソート、関数ポインタ、`long` での線形合同法 |

## 制限

- `unsigned`、浮動小数点 (そもそも `../emu` は RV64IMAC で F/D 拡張なし)、`union`、ビットフィールド、`goto`、
  `const` は読み飛ばすだけ (検査しない)、関数内の `static` 変数、K&R 形式は未対応
- プリプロセッサなし (`#define` も無視されるので、マクロを使うプログラムは動かない)
- struct の値渡し・値返し (ポインタを使う)。struct の代入とコピー初期化はできる
- 引数は 8 個まで (スタック渡し未対応)。可変長引数も合わせて 8 個まで。`__va_start()` は rvcc 独自
- 初期化子の波括弧の省略 (`int a[2][2] = {1, 2, 3, 4}`) や指示付き初期化子 (`.x = 1`) は未対応
- 型検査は甘い (`int *` と `char *` の混在や、整数とポインタの代入を許す)
- `malloc` は解放しない。`printf` は 1 回の出力が 256 バイトを超えると途中で write する (順序は保たれる)

## 次の一歩

- **レジスタ割り当て**: 今はローカル変数がすべてメモリ (`fp` 相対) にあります。アドレスを取られない変数を
  s レジスタに置くだけで fib の `sw`/`lw` が消え、もう一段減るはずです
- 構造体の値渡し (psABI では 16 バイト以下はレジスタ 2 本)、スタック渡しの引数、`unsigned` (`divu` `sltu` `srl`)
- 小さなプリプロセッサ (`#define` 定数と `#include "..."` だけ)
- **`../os` のユーザプログラムを作る**: zigos は `a7` に番号、`a0`〜 に引数を入れて `ecall` する点は同じですが、
  番号が違います (exit=0, read=1, write=2, open=3 …)。`crt0.s` のシステムコール番号を差し替えた
  `crt0_zigos.s` を用意すれば、libc.c はそのまま使えます (brk が無いので `malloc` は `.bss` の固定領域からのバンプ割り当てにする)。
  ただし zigos のユーザプログラムは今はカーネルと一緒にリンクされた関数なので、
  ELF ローダ (`exec`) をカーネル側に足すか、rvcc の出力をカーネルのビルドにリンクする必要があります
  (`_start` の代わりに zigos のプログラム表に `main` を登録する)。ページングが無いので、
  リンク先のアドレスをユーザ領域ごとに変える (`../asm` のリンカにベースアドレスの指定を足す) ことも必要です
