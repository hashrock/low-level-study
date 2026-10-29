# mininet — Go で書く学習用 TCP/IP スタック

Ethernet / ARP / IPv4 / ICMP / UDP / TCP を標準ライブラリだけで手書きした、小さな TCP/IP スタックです。
ヘッダのパース・組み立て・チェックサム計算・TCP の状態機械をすべて自前で実装しています。

macOS では TAP デバイスを root なしで使えないので、スタックは「Ethernet フレームを送受信できるもの」
(`LinkDevice`) の上に作り、その実体として **UDP で Ethernet フレームを運ぶ仮想ケーブル** を用意しました。
自作スタックを積んだプロセスを 2 つ立ち上げると、その間で ping・TCP・HTTP が通ります。
流れたフレームはすべて `.pcap` に保存でき、Wireshark で 1 バイトずつ確認できます。

## 動かし方

```sh
go vet ./... && go test -race ./...

# 端末 1: ノード A (10.0.0.1) — TCP echo :7, UDP echo :7, HTTP :80
go run . node --mac 02:00:00:00:00:01 --ip 10.0.0.1 \
    --wire-listen 127.0.0.1:9001 --wire-peer 127.0.0.1:9002 --pcap a.pcap serve

# 端末 2: ノード B (10.0.0.2) から色々送る
B="go run . node --mac 02:00:00:00:00:02 --ip 10.0.0.2 --wire-listen 127.0.0.1:9002 --wire-peer 127.0.0.1:9001"
$B ping 10.0.0.1 3
$B udp-echo 10.0.0.1 7 "hello"
$B -v tcp-echo 10.0.0.1 7 "hello"         # -v で各層のログ (状態遷移・送受信セグメント)
$B tcp-bulk 10.0.0.1 7 200000             # 200KB を echo させて SHA-256 で照合
$B --loss 0.1 -v tcp-bulk 10.0.0.1 7 100000  # 10% のフレームを捨てて再送を観察
$B http-get 10.0.0.1 80 /hello
```

## ブラウザ版 (web/net)

`web/net/build.sh` で `cmd/wasm` を wasm にビルドし、`python3 -m http.server -d web 8000` → `http://localhost:8000/net/`。
1 つの wasm の中で 2 つのスタック (A 10.0.0.1 サーバ / B 10.0.0.2 クライアント) を `SimWire` でつなぎ、
シーケンス図・パケットインスペクタ (層ごとの分解 + 色分け 16 進ダンプ)・TCP 状態機械・ARP キャッシュ/コネクション表・pcap ダウンロードを表示する。

- `mininet/simwire.go` — 遅延・損失・「次を落とす」・一時停止/1 フレームずつ配送・タップ付きの仮想ケーブル
- `mininet/decode.go` — フレームを層とフィールド (バイト範囲付き) に分解 (`Decode`, `Summarize`)
- `mininet/snapshot.go` — `Stack.Snapshot()` (ARP キャッシュ, TCP の snd.una/nxt, rcv.nxt, ウィンドウ, 再送キュー, RTO …)、`OnTCPState` フック
- `sim/` — 2 ノードの実験室 (イベントログ、相対 seq のラベル付け、再送・重複 ACK の判定、pcap 生成)。JS 非依存で `go test` できる
- `cmd/wasm/` — `window.mininet` に `poll / frame / run / setLoss / setLatency / dropNext / setPaused / step / dropHead / pcap / reset` を公開

## ファイル構成と層

スタック本体はパッケージ `mininet/mininet` (ディレクトリ `mininet/`)、CLI は `main.go`。


```
  アプリ      apps.go  (TCP echo / UDP echo / HTTP/1.0 サーバとクライアント), main.go (CLI)
 ─────────────────────────────────────────────────────────────────────────
  L4         icmp.go (ping)   udp.go (UDPConn)   tcp.go + tcp_conn.go (TCPConn / TCPListener)
 ─────────────────────────────────────────────────────────────────────────
  L3         ipv4.go  (IPv4 ヘッダ, インターネットチェックサム, 疑似ヘッダ)
 ─────────────────────────────────────────────────────────────────────────
  L2.5       arp.go   (IP → MAC 解決, キャッシュ, 解決待ちキュー)
 ─────────────────────────────────────────────────────────────────────────
  L2         ethernet.go (Ethernet II)      stack.go (受信ループ・層間の振り分け)
 ─────────────────────────────────────────────────────────────────────────
  L1 (仮想)  link.go  LinkDevice ── ChanWire (テスト用) / UDPWire (プロセス間) / LossyLink
                         └─ pcap.go (送受信の全フレームを .pcap へ)
```

受信の流れ: `rxLoop` → `handleFrame` (宛先 MAC 確認, EtherType で分岐) → `handleARP` / `handleIPv4`
(チェックサム・宛先 IP 確認, Protocol で分岐) → `handleICMP` / `handleUDP` / `handleTCP` → 各ソケット。

送信の流れ: `TCPConn.output` / `UDPConn.WriteTo` / `Ping` → `SendIP` (IPv4 ヘッダ付与) → `resolveAndSend`
(ARP) → `sendFrame` (Ethernet ヘッダ付与, pcap へ記録) → `LinkDevice.Send`。

## 仮想ケーブル (UDPWire) の仕組み

```
 ┌──────── プロセス A ────────┐                 ┌──────── プロセス B ────────┐
 │ mininet スタック 10.0.0.1  │                 │ mininet スタック 10.0.0.2  │
 │   │ Ethernet フレーム       │                 │       Ethernet フレーム │   │
 │ UDPWire 127.0.0.1:9001  ───┼── UDP データグラム ──┼─── UDPWire 127.0.0.1:9002 │
 └────────────────────────────┘ (中身 = フレーム丸ごと) └────────────────────────────┘
```

ホスト OS から見るとただの localhost の UDP 通信です。UDP のペイロードに Ethernet フレーム
(宛先 MAC から始まるバイト列) をそのまま 1 つ詰めます。ケーブル 1 本 = 2 ノード固定の point-to-point
なので、ブロードキャスト (ARP request) も単に相手に届くだけです。QEMU の `-netdev socket` や VDE と同じ発想です。

テストでは代わりに `ChanWire` (Go の channel 2 本) を使い、1 プロセス内で 2 つのスタックをつなぎます。
`LossyLink` は任意の `LinkDevice` を包んで、送信フレームを確率 p で捨てます。

## ヘッダレイアウト (バイトオフセット)

### Ethernet II (14 bytes)
```
 0        6        12     14
 | 宛先MAC | 送信元MAC | Type | ペイロード (46 bytes 未満なら 0 パディングして 60 bytes に)
 Type: 0x0800 IPv4, 0x0806 ARP     ※ FCS (CRC) は扱わない
```

### ARP (Ethernet/IPv4 用, 28 bytes)
```
 0      2      4    5    6      8        14     18       24     28
 |HType |PType |HLen|PLen| Op   |  SHA   | SPA  |  THA   | TPA  |
 HType=1, PType=0x0800, HLen=6, PLen=4, Op: 1=request 2=reply
 SHA/SPA = 送信者の MAC/IP, THA/TPA = 対象の MAC/IP (request では THA=0)
```

### IPv4 (20 bytes, オプションなし)
```
 0         1      2              4      6              8     9       10        12      16      20
 |Ver|IHL  | TOS  | Total Length | ID   |Flags|FragOff | TTL | Proto | Checksum | Src IP | Dst IP |
  4bit 4bit                              3bit  13bit           1/6/17
 Checksum: ヘッダ 20 bytes の 1 の補数和 (チェックサム欄込みで再計算すると 0 になれば正しい)
```

### ICMP Echo (8 bytes + データ)
```
 0      1      2          4     6     8
 | Type | Code | Checksum | ID  | Seq | Data...
 Type 8 = Echo Request, 0 = Echo Reply. Checksum は ICMP メッセージ全体
```

### UDP (8 bytes)
```
 0          2          4        6          8
 | Src Port | Dst Port | Length | Checksum | Data...
```

### TCP (20 bytes, オプションなし)
```
 0          2          4         8         12        13      14       16         18       20
 | Src Port | Dst Port | Seq Num | Ack Num |DOff|Rsvd| Flags | Window | Checksum | Urgent |
                                             4bit        CWR ECE URG ACK PSH RST SYN FIN
```

### TCP/UDP の疑似ヘッダ (チェックサム計算にだけ使う, 12 bytes)
```
 0        4        8      9       10       12
 | Src IP | Dst IP | 0x00 | Proto | Length | + TCP/UDP セグメント本体
```

## ARP の動き

```
 B: SendIP(10.0.0.1, ...)
      │ キャッシュにない
      ├─ パケットを pending[10.0.0.1] に積む
      └─ ARP request (宛先 MAC ff:ff:ff:ff:ff:ff) "who-has 10.0.0.1 tell 10.0.0.2"
                                         A: 自分宛て → B の MAC をキャッシュ (RFC 826 のマージ)
                                            ARP reply "10.0.0.1 is-at 02:..:01"
 B: キャッシュに登録 → pending をまとめて送信
```
request への応答が無い場合、次に同じ宛先へ送ろうとした時に (前回から 300ms 以上経っていれば) request を再送します。
キャッシュに有効期限はありません。

## TCP の状態遷移

```
                              ┌────────┐
            ┌─────────────────│ CLOSED │──────────────────┐
            │ ListenTCP       └────────┘   DialTCP        │
            ▼                                 送: SYN      ▼
       ┌────────┐ 受: SYN                            ┌──────────┐
       │ LISTEN │ 送: SYN+ACK                         │ SYN_SENT │
       └────────┘──────┐                             └──────────┘
                       ▼                                  │ 受: SYN+ACK
                 ┌──────────┐  受: ACK                    │ 送: ACK
                 │ SYN_RCVD │───────────┐                 │
                 └──────────┘           ▼                 ▼
                                  ┌─────────────────────────────┐
                                  │         ESTABLISHED          │
                                  └─────────────────────────────┘
               Close() 送: FIN │                       │ 受: FIN 送: ACK
                               ▼                       ▼
                        ┌────────────┐          ┌────────────┐
           受: FIN      │ FIN_WAIT_1 │          │ CLOSE_WAIT │
         ┌──────────────└────────────┘          └────────────┘
         ▼   送: ACK          │ 受: ACK (FIN に対する)  │ Close() 送: FIN
    ┌─────────┐               ▼                       ▼
    │ CLOSING │        ┌────────────┐           ┌──────────┐
    └─────────┘        │ FIN_WAIT_2 │           │ LAST_ACK │
         │ 受: ACK     └────────────┘           └──────────┘
         │                    │ 受: FIN 送: ACK       │ 受: ACK
         ▼                    ▼                       ▼
    ┌─────────────────────────────┐             ┌────────┐
    │ TIME_WAIT (既定 1 秒に短縮)  │────────────▶│ CLOSED │
    └─────────────────────────────┘ タイムアウト └────────┘
```
`-v` を付けると `state ESTABLISHED -> FIN_WAIT_1` のように遷移がログに出ます。

### 送信側のシーケンス空間と再送

```
      sndUna             sndNxt                   sndUna+len(sendBuf)
 ───────┼──────────────────┼──────────────────────────┼─────
  ACK 済 │ 送信済・未 ACK     │ 未送信 (相手のウィンドウが空き次第) │
        │<───────────────── sendBuf ─────────────────>│
```
- 一度に送れるのは「相手が広告した受信ウィンドウ」まで。1 セグメントは最大 MSS = 1460 bytes。
- 受信ウィンドウは固定サイズ (65535 bytes) の受信バッファの空き容量。アプリが Read すると空く。
- 再送タイマは 1 本。RTO (初期 200ms, 倍々で最大 3 秒) が切れたら `sndNxt = sndUna` に巻き戻して送り直す (Go-Back-N)。
  新しい ACK が来たら RTO を初期値に戻す。12 回連続で失敗したら `connection timed out`。
- 重複 ACK を 3 回受けたらタイムアウトを待たずに同じく巻き戻す (高速再送)。
- 相手のウィンドウが 0 の時はタイマ満了で 1 バイトだけ送ってウィンドウを問い合わせる (ゼロウィンドウ・プローブ)。
- 受信側は `rcvNxt` ちょうどのセグメントだけを受け取り、順序外のものは捨てて ACK (重複 ACK) を返します。
- `Close()` は送信方向だけを閉じ (FIN を送る)、相手の FIN が来るまで `Read` を続けられます (half-close)。

## pcap を Wireshark で開く

`--pcap file.pcap` を付けると、そのノードが送った/受け取ったフレームがすべて記録されます
(libpcap 形式, LINKTYPE_ETHERNET, マイクロ秒精度)。

```sh
tcpdump -r a.pcap -n          # 要約
tcpdump -r a.pcap -n -v -X    # IP ヘッダの詳細 + 16 進ダンプ。cksum ... (correct) と出れば自作チェックサムが正しい
open -a Wireshark a.pcap      # Wireshark (インストールされていれば)
```
Wireshark では上のペインでパケットを選び、中央ペインで各ヘッダを展開すると、下のペインの対応するバイトが
ハイライトされます。上記のオフセット表と照らし合わせると理解が深まります。
`Statistics → Flow Graph` (TCP フロー) にすると 3-way handshake と FIN の往復が図で見られます。
`--loss` を付けた場合、送信側の pcap には捨てられたフレームも記録されます (送信前に記録するため)。
受信側の pcap を見ると、実際に届いたものと Wireshark の `[TCP Retransmission]` 表示が確認できます。

## テスト

`stack_test.go` (すべて ChanWire 上でプロセス内):
- チェックサム (RFC 1071 の例), IPv4/TCP ヘッダの往復
- ARP 解決 (双方のキャッシュ確認), ping, 存在しない相手への ping のタイムアウト
- UDP echo
- 3-way handshake → データ → active/passive close → 両側 CLOSED, コネクション表が空になる
- 閉じたポートへの接続が RST で拒否される
- 100KB の TCP echo (受信ウィンドウ 64KB より大きい) と完全一致確認
- 両方向で 10% のフレームを落とすリンク上で 100KB echo (シード 3 種) — 再送なしでは成立しない
- HTTP/1.0 GET, pcap ファイルの形式

## 制限 (意図的に省いたもの)

- **IP フラグメントの再組み立てなし**: MF フラグ付き / offset≠0 のパケットは捨てる。送信は DF を立て、1500 bytes を超えない。
- **ルーティングなし**: 宛先はすべて同一リンク上とみなす (ゲートウェイ・サブネットマスクなし)。IP オプション非対応。
- **ARP**: キャッシュの有効期限なし、Gratuitous ARP なし。
- **ICMP**: Echo のみ (Destination Unreachable 等は送らない)。閉じた UDP ポート宛ては黙って捨てる。
- **TCP**:
  - 輻輳制御なし (slow start / cwnd / AIMD なし)。相手の受信ウィンドウぶん一気に送る。
  - RTT 計測なし (RTO は固定初期値 + 指数バックオフ)。Karn のアルゴリズムも不要な作り。
  - 順序外セグメントを保持しない (捨てて再送を待つ)。SACK なし。
  - オプションを送らない (MSS は 1460 固定, Window Scale / Timestamps なし)。受信時は読み飛ばす。
  - 遅延 ACK なし (データを受けるたびに即 ACK), Nagle なし。
  - 同時オープン (SYN_SENT 中に SYN を受ける) 非対応。FIN_WAIT_2 のタイムアウトなし。TIME_WAIT は既定 1 秒。
  - ISN は乱数 (RFC 6528 のハッシュ方式ではない)。RST のシーケンス番号検証は省略。
- **仮想ケーブル**: 2 ノードの point-to-point のみ。

## 次のステップ案

1. **輻輳制御**: cwnd を導入して slow start → congestion avoidance → (高速再送時) fast recovery。`--loss` で効果を比較。
2. **RTT 計測と RTO 計算** (RFC 6298: SRTT/RTTVAR)。
3. **順序外キュー** と SACK。損失時の Go-Back-N の無駄な再送が減るのを pcap で確認。
4. **MSS オプション**, Window Scale。
5. **IP フラグメントの再組み立て**、ICMP Destination Unreachable / Port Unreachable。
6. **複数ノードのハブ/スイッチ**: UDPWire を 1 対多にして、MAC 学習するスイッチ (L2) を書く。さらにルータ (L3) を書けばサブネット越えの通信。
7. **macOS utun で実 OS とつなぐ** (要 sudo): `socket(AF_SYSTEM, SOCK_DGRAM, SYSPROTO_CONTROL)` で
   `com.apple.net.utun_control` に connect すると utun デバイスができる。utun は L3 (先頭 4 bytes にアドレスファミリ、
   その後に IP パケット) なので、Ethernet/ARP を飛ばして `handleIPv4` / `SendIP` を直接つなぐ `IPDevice` を作る。
   `sudo ifconfig utunN 10.0.0.2 10.0.0.1 up` の後、macOS 本物の `ping 10.0.0.1` や `curl http://10.0.0.1/` が自作スタックに届く。
   (Linux なら TAP デバイスで L2 からそのままつなげる。)
