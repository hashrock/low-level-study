# minidb — 学習用の小さなリレーショナルデータベース

Go 標準ライブラリだけで書いた、約 1900 行 (テスト・観察用コード除く) のおもちゃ RDB です。
「SQL 文字列がどうやってディスク上のバイト列になるのか」を端から端まで追えることを目標にしています。

## 動かし方

```sh
go run .              # minidb.db を開く (無ければ作る)
go run . my.db        # 任意のファイル
go test ./...         # テスト
../web/db/build.sh    # ブラウザ版 (web/db/main.wasm) をビルド
```

REPL では SQL を `;` で終えると実行されます。メタコマンド: `.tables` `.schema` `.exit`。
パイプ入力 (`cat x.sql | go run .`) も可能です (このときプロンプトは表示されません)。

### セッション例

```
minidb> CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER);
CREATE TABLE
minidb> CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title TEXT);
CREATE TABLE
minidb> INSERT INTO users VALUES (1, 'alice', 30), (2, 'bob', 25), (3, 'carol', 41);
INSERT 3
minidb> INSERT INTO users (name, age) VALUES ('dave', 19);   -- id は自動採番
INSERT 1
minidb> INSERT INTO posts VALUES (10, 1, 'Hello'), (11, 3, 'B-tree入門'), (12, 1, 'Pager');
INSERT 3
minidb> SELECT * FROM users
   ...>   WHERE age >= 20
   ...>   ORDER BY age DESC;
+----+-------+-----+
| id | name  | age |
+----+-------+-----+
| 3  | carol | 41  |
| 1  | alice | 30  |
| 2  | bob   | 25  |
+----+-------+-----+
(3 rows)
minidb> INSERT INTO users VALUES (1, 'dup', 0);
Error: insert id=1: duplicate primary key
minidb> .exit
```

一度終了して再度起動しても、データは残っています:

```
minidb> UPDATE users SET age = 26 WHERE name = 'bob';
UPDATE 1
minidb> DELETE FROM users WHERE age < 20;
DELETE 1
minidb> SELECT users.name, posts.title FROM posts JOIN users ON posts.user_id = users.id ORDER BY posts.id;
+------------+-------------+
| users.name | posts.title |
+------------+-------------+
| alice      | Hello       |
| carol      | B-tree入門  |
| alice      | Pager       |
+------------+-------------+
(3 rows)
minidb> .schema
CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER); -- root page 1
CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title TEXT); -- root page 2
```

## 対応している SQL

```sql
CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, ...);   -- INTEGER 主キーが 1 つ必須
INSERT INTO t [(col, ...)] VALUES (...), (...);              -- 主キー省略時は max+1
SELECT * | col, t.col, ... FROM t [[INNER] JOIN u ON expr]
       [WHERE expr] [ORDER BY col [ASC|DESC]] [LIMIT n];
UPDATE t SET col = expr, ... [WHERE expr];
DELETE FROM t [WHERE expr];
```

式: 整数・'文字列'・NULL・カラム参照、比較 `= != <> < > <= >=`、`AND` `OR`、括弧。
NULL との比較は常に偽になります。

## アーキテクチャ

上から下へ、1 層 = 1 ファイル程度に分かれています。

```
 SQL 文字列
    │  lexer.go     字句解析: 文字列 → トークン列
    ▼
 []Token
    │  parser.go    構文解析 (再帰下降): トークン列 → AST  (ast.go)
    ▼
 Statement (AST)
    │  executor.go  実行: AST を解釈し、テーブルの B+tree を操作。WHERE/ORDER BY/JOIN もここ
    ▼
 Table / 行 (record.go で []Value ⇔ []byte)
    │  btree.go     B+tree: 主キー(int64) → 行バイト列。1 ノード = 1 ページ
    ▼
 ページ
    │  pager.go     ページャ: ファイルを 4096 バイトのページ配列として読み書き + LRU キャッシュ
    ▼
 ディスク上の 1 ファイル
```

本体はインポート可能なパッケージ `minidb/minidb` (ディレクトリ `minidb/`) にあり、
`main.go` (REPL) と `cmd/wasm` (ブラウザ版) はそれを使うだけです。

| ファイル | 役割 |
|---|---|
| `main.go` | REPL、結果の表形式表示 |
| `minidb/lexer.go` / `ast.go` / `parser.go` | SQL → AST |
| `minidb/executor.go` | 文の実行、式の評価、主キー検索の最適化 |
| `minidb/db.go` | DB の Open/Close、カタログ (ページ 0) の読み書き |
| `minidb/record.go` | 値 (`Value`) と行のエンコード |
| `minidb/btree.go` | B+tree (検索・挿入と分割・削除・スキャン) |
| `minidb/pager.go` | ページ単位の I/O とキャッシュ。ファイルは `File` インターフェース越しに扱う |
| `minidb/memfile.go` | メモリ上の `File` 実装 (ブラウザ版・テスト用) |
| `minidb/stats.go` | 文ごとの統計 (触ったページ、主キー検索か全件スキャンか、読んだ行数) |
| `minidb/inspect.go` | 内部を覗く API (テーブル一覧、木のダンプ、ページの注釈付きダンプ、キャッシュ状態) |
| `cmd/wasm/main.go` | ブラウザ版のエントリポイント (`GOOS=js GOARCH=wasm` 専用) |

### ブラウザ版 (web/db)

`web/db/build.sh` で `web/db/main.wasm` をビルドし、`python3 -m http.server -d web 8000` で
`http://localhost:8000/db/` を開くと、SQL コンソール・B+tree の図・ページのヘックスダンプ・
キャッシュと文ごとの統計を見ながら操作できます。DB はメモリ上の `MemFile` に置かれ、
「.db を保存」でダウンロードしたファイルはこの CLI でそのまま開けます (逆も可)。

観察用 API (どれもページャの LRU 順やカウンタを変えません):

```go
db, _ := minidb.OpenMem(nil)          // OpenFile(f File, size) / Open(path) もある
db.Exec("SELECT * FROM users WHERE id = 5")
db.LastStats()                        // []*StmtStats: access (pk-lookup/full-scan/pk-insert), rowsExamined,
                                      //   pageGets, diskReads/Writes, touched/dirtied/allocated, splits ...
db.TableInfos()                       // テーブルとルートページ
db.TreeDump("users")                  // *TreeNode: page, type, keys, children, next, used/free
db.PageInfo(3)                        // 生バイト + バイト範囲の注釈 + デコードしたセル
db.PageBytes(3)
db.CacheState()                       // LRU 順のキャッシュ中ページ、dirty、累計カウンタ
db.SetCacheCapacity(4)
db.Snapshot()                         // DB ファイル全体のバイト列
```

### ファイルとページ

DB は 1 つのファイルで、4096 バイトの固定長ページが並んでいるだけです。
ページ `n` はファイルのオフセット `n * 4096` にあります。新しいページは常にファイル末尾に追加されます。

```
ファイル: [ page 0 | page 1 | page 2 | page 3 | ... ]
            カタログ  usersの   postsの   B+treeの
                     ルート    ルート    分割で増えたノード
```

**ページャ (minidb/pager.go)** はページ ID を受け取ってメモリ上の `Page` を返します。
最大 64 ページを LRU でキャッシュし、溢れたら最も古いページを (変更されていれば書き戻してから) 捨てます。
SQL 文を 1 つ実行するたびに、変更されたページ (dirty) を全部書き出して `fsync` します。

### ページ 0: カタログ

テーブル定義の一覧です。各テーブルの「B+tree のルートページ ID」を覚えているのがポイントです。

```
[0:8]   "MINIDB01" (マジックナンバー)
[8:10]  テーブル数 uint16
テーブルごと:
  名前長 uint8, 名前, ルートページID uint32, カラム数 uint8,
  カラムごと: 名前長 uint8, 名前, 型 uint8 (1=INTEGER, 2=TEXT), 主キー uint8
```

### 行のエンコード (record.go)

行 (= カラム値の並び) は、各値を `[型タグ 1byte][中身]` で連結したバイト列です。

```
NULL    : 00
INTEGER : 01 + int64 (8 bytes, little endian)
TEXT    : 02 + 長さ uint16 + UTF-8 バイト列

例: (1, 'bob', NULL) → 01 0100000000000000 | 02 0300 626f62 | 00
```

### B+tree (btree.go)

各テーブルは「主キー → 行バイト列」の B+tree として保存されます。1 ノード = 1 ページです。

```
                    ┌──────────────────────┐
     内部ノード      │ c0 | 100 | c1 | 200 | c2 │   (キー k の行は keys[i] <= k < keys[i+1] の子へ)
                    └──┬────────┬────────┬───┘
                       ▼        ▼        ▼
     葉ノード      [1..99] → [100..199] → [200..]    (葉は next で右隣とつながる)
                  key+行     key+行      key+行
```

ページ上のレイアウト:

```
[0]     種別 (1 = 葉, 2 = 内部)
[1:3]   キー数 n (uint16)
[3:7]   next: 右隣の葉のページ ID (葉のみ。0 = なし)
[7:]    葉  : n 個の (key int64 | 値の長さ uint16 | 値)
        内部: child0 uint32, 続いて n 個の (key int64 | child uint32)
```

- **検索**: ルートから、各内部ノードでキーを二分探索して子へ降り、葉で二分探索。
  `WHERE id = 42` のような主キーの等値条件は全件スキャンせずこの経路を使います (`executor.go` の `pkEquality`)。
- **挿入**: 葉に入れ、ページ (4096 バイト) に収まらなくなったら**分割**します。
  - 葉の分割: バイト数が半分くらいになる位置で左右に分け、右の最小キーを親に「コピー」。
  - 内部ノードの分割: 真ん中のキーを親に「移動」。
  - 分割は再帰的に親へ伝わり、ルートまで溢れたら木が 1 段高くなります。
    このときルートの中身を新しいページへ移し、ルートページ自体は新しい内部ノードにするので、
    **ルートのページ ID は変わらず**、カタログを更新する必要がありません。
- **全件スキャン**: 一番左の葉まで降りて、`next` ポインタを辿るだけ。キー順に行が出てきます。
- **削除**: 葉からキーを消すだけです (併合・再配分はしない)。
- 実装を読みやすくするため、ノードは読むたびに Go の構造体 (`node`) にデコードし、変更後にエンコードし直しています。

### 実行 (executor.go)

- `SELECT`: 行を集める → WHERE で絞る → ORDER BY で並べ替え → LIMIT → 必要なカラムを射影。
- `JOIN`: 右テーブルを全部メモリに読み込み、左の各行と全組み合わせを試すネステッドループ結合。
- `UPDATE` / `DELETE`: まず対象行を集めてから木を変更します (走査中に木を壊さないため)。
  UPDATE は「削除 + 挿入」で実装しています。
- 式の評価結果の真偽は INTEGER の 1/0 で表します。

## 既知の制限

- **トランザクション・クラッシュ耐性なし**。文の途中でエラーになると、それまでの変更は残ります
  (例: 複数行 INSERT の途中で主キー重複)。書き込み中にプロセスが落ちるとファイルが壊れる可能性があります。
- 主キーは INTEGER 1 カラムのみ必須。セカンダリインデックスなし (主キー以外の WHERE は全件スキャン)。
- 削除してもページは再利用されず、空の葉も残ります (ファイルは縮まない)。
- 1 行は最大 1000 バイト (オーバーフローページ未実装)。カタログは 1 ページに収まる分だけ。
- 自動採番は全件スキャンで最大キーを探すので遅い。
- 型は INTEGER と TEXT のみ。算術演算・集約関数 (COUNT 等)・GROUP BY・DROP TABLE・テーブル別名なし。
  JOIN は 2 テーブルの内部結合のみで、自己結合はできません。
- 同時アクセス (複数プロセス・ロック) は考慮していません。

## 次のステップの案

1. **WAL / ロールバックジャーナル**: 変更前のページをジャーナルに書いてから本体を更新し、
   クラッシュ時に復元する。`BEGIN` / `COMMIT` / `ROLLBACK` もこの上に作れます。
2. **スロット付きページ (slotted page)**: ノードを毎回デコードせず、ページ上で直接セルを挿入・削除する。
3. **削除時のノード併合とフリーリスト**: 空いたページを再利用する。
4. **セカンダリインデックス**: 「カラム値 → 主キー」の B+tree を追加し、オプティマイザで使い分ける。
5. **オーバーフローページ**: 大きな TEXT を複数ページにまたがって保存する。
6. **範囲スキャン**: `WHERE id > 100` で葉の途中からスキャンを始める。
