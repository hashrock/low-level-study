# minilang

「JavaScript っぽい見た目で、もっと素直な」小さな言語です。Go 標準ライブラリだけで、
**字句解析 → Pratt パーサ → AST → バイトコードコンパイラ → スタック VM** を一通り実装しています。

```sh
go run . examples/fib.lang        # ファイルを実行
go run . -dis examples/fib.lang   # バイトコードを逆アセンブルして表示
go run .                          # REPL
go test ./...                     # examples/*.lang を実行して *.out と比較
```

## 言語仕様のまとめ

```js
// 変数 (ブロックスコープ、巻き上げなし)
let x = 1
const name = "minilang"      // 再代入はエラー

// 関数 (宣言・無名関数・式1つなら =>)
fn add(a, b) { return a + b }
const sq = fn(x) => x * x
fn adder(n) => fn(x) => x + n      // クロージャ

// 制御構文
if (x > 0) { print("pos") } else if (x == 0) { print("zero") } else { print("neg") }
while (x < 10) { x += 1 }
for (v in [1, 2, 3]) { print(v) }   // 配列は「要素」を列挙
for (k in {a: 1, b: 2}) { print(k) } // マップは「キー」を挿入順に列挙
// break / continue / return

// データ
let arr = [1, 2, 3]
let m = {name: "Alice", "with space": 1}
m.age = 30           // m["age"] = 30 と同じ
print(arr[0], m.name)
```

- セミコロンは省略可能 (書いてもよい)。コメントは `//`。
- 型: `nil` `bool` `number` `string` `array` `map` `function` の 7 つ。
- 演算子: `+ - * / %`、`== != < <= > >=`、`&& || !`、代入 `= += -= *= /=`。
  `&&` `||` は短絡評価で、オペランドの値そのものを返す (`nil || "d"` は `"d"`)。
- 組み込み関数: `print(...)` `len(x)` `push(arr, v)` `pop(arr)` `keys(map)` `has(map, key)`
  `type(v)` `str(v)` `range(n)` / `range(from, to)`。
- エラーは `行:列: 種類: メッセージ` 形式。関数の中で起きた実行時エラーにはスタックトレースが付きます。

## JS との違いとその理由

| 項目 | JavaScript | minilang | 理由 |
|---|---|---|---|
| 暗黙の型変換 | `1 + "a"` → `"1a"`、`"3" * 2` → `6` | **実行時エラー** | 型の混在はほぼ常にバグ。変換は `str()` で明示する |
| 等価比較 | `==` (変換あり) と `===` | `==` のみ (型が違えば `false`) | 2 種類あること自体が罠 |
| 「無い」値 | `undefined` と `null` | `nil` だけ | 区別しても得をする場面が少ない |
| 真偽値 | `0` `""` `NaN` `null` `undefined` `false` が偽 | `false` と `nil` だけが偽 | `if (count)` が 0 で偽になる類のバグを防ぐ (Ruby/Lua と同じ) |
| 存在しないキー / 範囲外 | 黙って `undefined` | **実行時エラー** (`has()` で事前確認) | 静かに伝播する `undefined` は原因の特定が難しい |
| 変数宣言 | `var` の巻き上げ、関数スコープ | `let`/`const` のみ、ブロックスコープ、巻き上げなし | 「書いた順に存在する」のが素直 |
| 未宣言変数への代入 | 暗黙のグローバル生成 (非 strict) | エラー | タイプミスを即座に検出 |
| `for-in` | 配列でも「キー(文字列)」を列挙 | 配列は要素、マップはキー | 一番よく欲しいものを返す |
| ループ内クロージャ | `var` だと全部同じ値を捕捉 | 反復ごとに新しい束縛 | 直感どおりに動く |
| `this` | 呼び方で変わる | **存在しない** | `m.f(x)` は「`m.f` を取り出して呼ぶ」だけ。状態はクロージャで持つ (examples/closures.lang の `makeAccount`) |
| ゼロ除算 | `Infinity` / `NaN` | エラー | `NaN` は比較が壊れる値なので作らない |
| 引数の個数 | 過不足は黙って無視 | 一致しないとエラー | 呼び出しミスを早く見つける |

### 数値型は 1 つ (float64)

数値は `number` 型 (Go の `float64`) の 1 種類だけです。

- 学習用の小さな言語として、整数/浮動小数の 2 種類にすると演算ごとの型の組み合わせ
  (int+float の昇格規則など) が増え、「暗黙の変換をしない」方針とぶつかるため。
- 2^53 までの整数は正確に表せるので、ループやインデックスで困ることはありません。
- 表示時は整数値なら小数点なしで出します (`7 / 2` → `3.5`、`6 / 2` → `3`)。
- インデックスには整数値の number が必要です (`a[1.5]` はエラー)。

## アーキテクチャ

```
ソース ──Lex──▶ []Token ──Parse──▶ AST ──Compile──▶ *Proto (バイトコード) ──VM.Run──▶ 結果
         lexer.go         parser.go/ast.go  compiler.go     opcode.go            vm.go
```

| ファイル | 役割 |
|---|---|
| `lexer.go` | 文字列 → トークン列。位置 (行:列) と共通エラー型 `LangError` |
| `ast.go` | AST ノード定義 |
| `parser.go` | Pratt パーサ。中置演算子ごとに優先順位を持ち、`expr(prec)` が再帰する |
| `compiler.go` | AST → バイトコード。変数をローカル/捕捉変数/グローバルに解決し、ジャンプ先をパッチする |
| `opcode.go` | 命令セット、`Proto` (関数 1 個分のコード)、逆アセンブラ |
| `value.go` | 実行時の値の型と表示 |
| `vm.go` | スタックマシン本体 (命令ディスパッチ、関数呼び出し、演算) |
| `builtins.go` | 組み込み関数 |
| `main.go` | ファイル実行と REPL |

### 変数とクロージャの仕組み

- トップレベル (ブロックの外) の宣言は **グローバル** (名前の表で管理)。それ以外は **ローカル** で、
  コンパイル時にスロット番号が決まり、`GET_LOCAL 3` のように番号でアクセスします。
- ローカル変数のスロットには値そのものではなく **セル (`*Cell`、値を 1 つ入れた箱)** を置きます。
  クロージャは作られた時点で外側のセルへのポインタを `Free` 配列にコピーします。
  同じセルを共有するので、関数から戻った後も変数は生き続け、複数のクロージャで共有できます。
  (Lua の upvalue の open/close より単純な代わりに、宣言ごとに小さな割り当てが発生します。)
- `let` を実行するたびに `DEF_LOCAL` が **新しいセル** を作るので、ループ内の `let` や for-in の
  ループ変数は反復ごとに別物になります。
- `fn name() {}` をローカルで宣言すると、先に空のセルを作ってからクロージャを作るので自己再帰できます。

### 関数呼び出しとスタック

```
CALL n の直前:   [... | f | arg0 | arg1 | ... argn-1 ]
                                                     ^sp
呼び出し後:      [... | f | cell(arg0) ... | nil ... | 一時値 ... ]
                        ^base (ローカル 0 番)  (NumLocals 個)
RETURN:          戻り値を pop し、sp = base-1 まで捨ててから戻り値を push
```

各フレームは `(closure, ip, base)` を持ちます。エラー時はフレームを上から辿ってスタックトレースを作ります。

## バイトコード命令一覧

1 命令 = 1 バイトのオペコード + 2 バイト (big endian) のオペランド 0〜3 個。
`go run . -dis file.lang` で実際のコードを見られます。

| 命令 | オペランド | スタック変化 / 動作 |
|---|---|---|
| `CONST` | k | → 定数表[k] |
| `NIL` `TRUE` `FALSE` | | → 値 |
| `POP` | | a → |
| `GET_LOCAL` | slot | → セル[slot] の値 |
| `SET_LOCAL` | slot | a → a (セルに代入、pop しない) |
| `DEF_LOCAL` | slot | a → (新しいセルを作って slot に置く) |
| `GET_FREE` / `SET_FREE` | i | クロージャが捕捉した i 番目のセルを読む / 書く |
| `GET_GLOBAL` / `SET_GLOBAL` | k(名前) | グローバルを読む / 書く (未定義・const はエラー) |
| `DEF_GLOBAL` | k(名前), const | a → (グローバルを定義) |
| `ADD` `SUB` `MUL` `DIV` `MOD` | | a b → a op b (型が合わなければエラー) |
| `EQ` `NE` `LT` `LE` `GT` `GE` | | a b → bool |
| `NEG` `NOT` | | a → -a / !a |
| `JUMP` | addr | ip = addr |
| `JUMP_IF_FALSE` | addr | a → (偽ならジャンプ) |
| `JUMP_IF_FALSE_OR_POP` | addr | `&&` 用: 偽なら残してジャンプ、真なら pop |
| `JUMP_IF_TRUE_OR_POP` | addr | `\|\|` 用: 真なら残してジャンプ、偽なら pop |
| `CALL` | n | f a1..an → 戻り値 |
| `RETURN` | | 戻り値を持ってフレームを抜ける |
| `CLOSURE` | k | → 定数表[k] の Proto と捕捉セルからクロージャを作る |
| `ARRAY` | n | v1..vn → 配列 |
| `MAP` | n | k1 v1 .. kn vn → マップ |
| `INDEX` | | obj key → obj[key] |
| `SET_INDEX` | | obj key v → v (obj[key] = v) |
| `ITER_PREP` | | 配列 → そのまま / マップ → キーの配列 |
| `FOR_ITER` | c, i, addr | 隠しローカル c (コレクション) の i 番目を積んで i++。終端なら addr へ |

例: `for (x in xs) { body }` は次のように展開されます。

```
        GET_GLOBAL xs ; ITER_PREP ; DEF_LOCAL c
        CONST 0       ; DEF_LOCAL i
loop:   FOR_ITER c i end
        DEF_LOCAL x          ; 反復ごとに新しいセル
        ...body...
        JUMP loop            ; continue もここへ
end:                         ; break はここへ
```

## 既知の制限

- **巻き上げがない** ので、関数内でローカルに宣言した関数同士の相互再帰はできません
  (後に宣言する関数が見えないため)。トップレベルの関数はグローバルなので相互再帰できます。
- 数値リテラルは `123` と `1.5` のみ (指数表記・16 進なし)。文字列補間なし、エスケープは `\n \t \" \\` のみ。
- マップのキーは文字列のみ。配列・マップの `==` は同一性比較 (中身は比較しない)。
- `a[f()] += 1` は `a[f()] = a[f()] + 1` に展開されるため `f` が 2 回呼ばれます。
- `return` の直後で改行しても次の行が戻り値として読まれます (セミコロン省略の副作用)。
- 循環参照するデータを `print` すると無限再帰します。
- 組み込み関数からユーザ関数を呼び戻す仕組みはないので、`map`/`filter` は言語側で書きます
  (examples/higher_order.lang)。
- GC は Go に任せています。最適化 (定数畳み込み、スロット再利用など) はしていません。
