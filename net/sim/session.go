// Package sim は 2 つの mininet スタックを SimWire でつないだ「実験室」。
// ブラウザ版 (cmd/wasm) から使う。JS には依存しないので普通の go test で検証できる。
//
//	ノード A 10.0.0.1 (サーバ: TCP echo :7, UDP echo :7, HTTP :80)
//	   │  SimWire (遅延・損失・一時停止・タップ)
//	ノード B 10.0.0.2 (クライアント: Run で操作を起動)
//
// 起きたこと (フレームの送受信・TCP 状態遷移・ログ・操作の結果) はすべて Event として
// 順番に記録され、Poll(since) で差分を取り出せる。呼び出しはどれもブロックしない
// (時間のかかる操作はゴルーチンで走り、結果は Event で届く)。
package sim

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"mininet/mininet"
)

var (
	IPA  = mininet.IP{10, 0, 0, 1}
	IPB  = mininet.IP{10, 0, 0, 2}
	MACA = mininet.MAC{0x02, 0, 0, 0, 0, 0x01}
	MACB = mininet.MAC{0x02, 0, 0, 0, 0, 0x02}
)

const (
	ClosedPort      = 81
	DefaultLatency  = 100 * time.Millisecond
	DefaultTimeWait = 2 * time.Second
	MaxBulkKB       = 512
)

// Event は 1 つの出来事。K で種類が決まり、使うフィールドが変わる。
//
//	tx    フレーム送信 (ID, Dir, Len, Proto, Label, Pair, Retx, Dup, Drop, Reason)
//	rx    フレーム到着 (ID)
//	drop  ケーブル上で捨てられた (ID, Reason)  ※送信時に捨てられたものは tx の Drop
//	state TCP 状態遷移 (Node, Pair, From, To)
//	log   スタックのログ (Node, Msg, Note: "rto" / "fast" / "probe" / "")
//	op    操作の開始/結果 (Op, Status: start/ok/err, Msg)
type Event struct {
	Seq    int     `json:"seq"`
	K      string  `json:"k"`
	T      float64 `json:"t"` // セッション開始からのミリ秒
	ID     uint64  `json:"id,omitempty"`
	Dir    string  `json:"dir,omitempty"` // "AB" or "BA"
	Len    int     `json:"len,omitempty"`
	Proto  string  `json:"proto,omitempty"` // arp icmp udp tcp
	Label  string  `json:"label,omitempty"`
	Flags  string  `json:"flags,omitempty"`
	Pair   string  `json:"pair,omitempty"`
	Retx   bool    `json:"retx,omitempty"`
	Dup    int     `json:"dup,omitempty"`
	Drop   bool    `json:"drop,omitempty"`
	Reason string  `json:"reason,omitempty"`
	Node   string  `json:"node,omitempty"`
	From   string  `json:"from,omitempty"`
	To     string  `json:"to,omitempty"`
	Msg    string  `json:"msg,omitempty"`
	Note   string  `json:"note,omitempty"`
	Op     int     `json:"op,omitempty"`
	Name   string  `json:"name,omitempty"`
	Status string  `json:"status,omitempty"`
}

// frameRec はセッション側で持つフレームの記録。WireFrame の可変フィールド (Dropped, DeliveredAt) は
// ワイヤ側のロックで守られているので、ここではコールバックで受け取った値をコピーして持つ。
type frameRec struct {
	f       *mininet.WireFrame // ID, Dir, Data, Summary, SentAt だけを読む (送信後は不変)
	label   string
	retx    bool
	dropped bool
	reason  string
	rxAt    time.Time
}

type Session struct {
	Wire  *mininet.SimWire
	A, B  *mininet.Stack
	start time.Time

	mu     sync.Mutex
	events []Event
	frames []frameRec // frames[id-1]
	flows  map[string]*flowState
	opSeq  int
	closed bool
}

// New はセッションを作り、A でサーバを起動する。
func New() *Session {
	s := &Session{start: time.Now(), flows: map[string]*flowState{}}
	w, da, db := mininet.NewSimWire(DefaultLatency, time.Now().UnixNano())
	s.Wire = w
	w.OnSend = s.onSend
	w.OnDeliver = func(f *mininet.WireFrame) {
		at := f.DeliveredAt
		s.update(f.ID, func(r *frameRec) { r.rxAt = at })
		s.add(Event{K: "rx", ID: f.ID, T: s.ms(at)})
	}
	w.OnDrop = func(f *mininet.WireFrame) {
		reason := f.DropReason
		s.update(f.ID, func(r *frameRec) { r.dropped, r.reason = true, reason })
		s.add(Event{K: "drop", ID: f.ID, T: s.now(), Reason: reason})
	}
	s.A = s.newStack("A", da, MACA, IPA)
	s.B = s.newStack("B", db, MACB, IPB)
	s.SetLatency(DefaultLatency)

	tl, _ := s.A.ListenTCP(7)
	hl, _ := s.A.ListenTCP(80)
	ul, _ := s.A.ListenUDP(7)
	go mininet.ServeTCPEcho(tl)
	go mininet.ServeHTTP(hl)
	go mininet.ServeUDPEcho(ul)
	return s
}

func (s *Session) newStack(node string, dev mininet.LinkDevice, mac mininet.MAC, ip mininet.IP) *mininet.Stack {
	st := mininet.NewStack(dev, mac, ip, nil)
	st.TimeWait = DefaultTimeWait
	st.OnTCPState = func(id mininet.TCPConnID, from, to mininet.TCPState) {
		s.add(Event{K: "state", T: s.now(), Node: node, Pair: pairOf(id.LocalIP, id.LocalPort, id.RemoteIP, id.RemotePort),
			From: from.String(), To: to.String()})
	}
	st.Logf = func(format string, args ...any) {
		// 送受信セグメントのログはシーケンス図で見えるので省く
		if strings.HasSuffix(format, "tx %v") || strings.HasSuffix(format, "rx %v") {
			return
		}
		msg := fmt.Sprintf(format, args...)
		note := ""
		switch {
		case strings.Contains(msg, "RTO expired"):
			note = "rto"
		case strings.Contains(msg, "fast retransmit"):
			note = "fast"
		case strings.Contains(msg, "zero window probe"):
			note = "probe"
		}
		s.add(Event{K: "log", T: s.now(), Node: node, Msg: msg, Note: note})
	}
	return st
}

// Close は両方のスタックとケーブルを閉じる (ブロックする)。
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.Wire.Close()
	s.A.Close()
	s.B.Close()
}

func (s *Session) ms(t time.Time) float64 { return float64(t.Sub(s.start).Microseconds()) / 1000 }
func (s *Session) now() float64           { return s.ms(time.Now()) }

func (s *Session) add(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	e.Seq = len(s.events)
	s.events = append(s.events, e)
}

func (s *Session) update(id uint64, fn func(r *frameRec)) {
	s.mu.Lock()
	if id >= 1 && int(id) <= len(s.frames) {
		fn(&s.frames[id-1])
	}
	s.mu.Unlock()
}

func pairOf(ip1 mininet.IP, p1 uint16, ip2 mininet.IP, p2 uint16) string {
	a, b := fmt.Sprintf("%v:%d", ip1, p1), fmt.Sprintf("%v:%d", ip2, p2)
	if b < a {
		a, b = b, a
	}
	return a + " ⇄ " + b
}

// ---------------------------------------------------------------------------
// 設定

func (s *Session) SetLoss(p float64) { s.Wire.SetLoss(min(max(p, 0), 1)) }

// SetLatency はケーブルの片道遅延を変え、それに合わせて両ノードの RTO も変える
// (RTO が往復時間より短いと、損失がなくても再送が起きてしまう)。
func (s *Session) SetLatency(d time.Duration) {
	d = min(max(d, 0), 5*time.Second)
	s.Wire.SetLatency(d)
	initial := max(200*time.Millisecond, 4*d+200*time.Millisecond)
	maxRTO := max(3*time.Second, 4*initial)
	s.A.SetRTO(initial, maxRTO)
	s.B.SetRTO(initial, maxRTO)
}

func (s *Session) DropNext()        { s.Wire.DropNext() }
func (s *Session) SetPaused(p bool) { s.Wire.SetPaused(p) }
func (s *Session) Step() bool       { return s.Wire.Step() }
func (s *Session) DropHead() bool   { return s.Wire.DropHead() }

// ---------------------------------------------------------------------------
// 操作 (B から A へ)。すぐに操作番号を返し、結果は op イベントで届く。

func (s *Session) Run(name, arg string) int {
	s.mu.Lock()
	s.opSeq++
	id := s.opSeq
	s.mu.Unlock()
	s.add(Event{K: "op", T: s.now(), Op: id, Name: name, Status: "start", Msg: arg})
	go func() {
		msg, err := s.run(name, arg)
		st := "ok"
		if err != nil {
			st = "err"
			if msg != "" {
				msg += "\n"
			}
			msg += "error: " + err.Error()
		}
		s.add(Event{K: "op", T: s.now(), Op: id, Name: name, Status: st, Msg: msg})
	}()
	return id
}

func (s *Session) rtt() time.Duration {
	return 2 * time.Duration(s.Wire.State().LatencyMs*float64(time.Millisecond))
}

func (s *Session) run(name, arg string) (string, error) {
	switch name {
	case "ping":
		var out strings.Builder
		ok := 0
		for i := range 3 {
			rtt, err := s.B.Ping(IPA, uint16(i), 56, 2*s.rtt()+2*time.Second)
			if err != nil {
				fmt.Fprintf(&out, "icmp_seq=%d timeout\n", i)
			} else {
				ok++
				fmt.Fprintf(&out, "64 bytes from %v: icmp_seq=%d time=%.1f ms\n", IPA, i, float64(rtt.Microseconds())/1000)
			}
			time.Sleep(300 * time.Millisecond)
		}
		fmt.Fprintf(&out, "3 packets transmitted, %d received", ok)
		if ok == 0 {
			return out.String(), errors.New("no reply")
		}
		return out.String(), nil

	case "udp":
		if arg == "" {
			arg = "hello udp"
		}
		u, err := s.B.ListenUDP(0)
		if err != nil {
			return "", err
		}
		defer u.Close()
		for range 3 { // 最初は ARP 解決待ちなので数回試す
			u.WriteTo([]byte(arg), IPA, 7)
			data, from, port, err := u.ReadFrom(2*s.rtt() + time.Second)
			if err == nil {
				return fmt.Sprintf("reply from %v:%d: %q", from, port, data), nil
			}
		}
		return "", errors.New("no reply")

	case "tcp-echo":
		if arg == "" {
			arg = "hello"
		}
		got, err := mininet.TCPEchoClient(s.B, IPA, 7, []byte(arg))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("echo reply: %q", got), nil

	case "bulk":
		var kb int
		fmt.Sscan(arg, &kb)
		kb = min(max(kb, 1), MaxBulkKB)
		data := make([]byte, kb*1024)
		rand.Read(data)
		start := time.Now()
		got, err := mininet.TCPEchoClient(s.B, IPA, 7, data)
		if err != nil {
			return "", err
		}
		h1, h2 := sha256.Sum256(data), sha256.Sum256(got)
		msg := fmt.Sprintf("sent %d bytes, received %d bytes in %v\nsha256 sent=%x…\nsha256 recv=%x…",
			len(data), len(got), time.Since(start).Round(time.Millisecond), h1[:8], h2[:8])
		if !bytes.Equal(data, got) {
			return msg, errors.New("data mismatch")
		}
		return msg + "\nintegrity OK", nil

	case "http":
		if arg == "" {
			arg = "/hello"
		}
		return mininet.HTTPGet(s.B, IPA, 80, arg)

	case "rst":
		c, err := s.B.DialTCP(IPA, ClosedPort)
		if err == nil {
			c.Close()
			return "", errors.New("connected?! (expected RST)")
		}
		if errors.Is(err, mininet.ErrConnReset) {
			return fmt.Sprintf("connect %v:%d: %v (RST を受け取った)", IPA, ClosedPort, err), nil
		}
		return "", err

	case "flush-arp":
		s.A.FlushARP()
		s.B.FlushARP()
		return "ARP キャッシュを消去しました", nil
	}
	return "", fmt.Errorf("unknown op %q", name)
}

// ---------------------------------------------------------------------------
// 観察

// PollResult は Poll の戻り値。
type PollResult struct {
	Now    float64               `json:"now"`
	Next   int                   `json:"next"` // 次回 Poll に渡す since
	Events []Event               `json:"events"`
	A      mininet.StackSnapshot `json:"A"`
	B      mininet.StackSnapshot `json:"B"`
	Wire   mininet.WireState     `json:"wire"`
	Frames int                   `json:"frames"`
}

// Poll は since 番目以降のイベントと、現在のスナップショットを返す。
// 一度に返すイベントは max 件まで (残りは次回)。
func (s *Session) Poll(since, max int) PollResult {
	s.mu.Lock()
	since = min(len(s.events), since)
	end := min(len(s.events), since+max)
	evs := append(make([]Event, 0, end-since), s.events[since:end]...)
	nframes := len(s.frames)
	s.mu.Unlock()
	// スタックのロックはセッションのロックを離してから取る (フックがセッションのロックを取るため)
	return PollResult{Now: s.now(), Next: end, Events: evs, A: s.A.Snapshot(), B: s.B.Snapshot(),
		Wire: s.Wire.State(), Frames: nframes}
}

// FrameDetail はインスペクタ用の詳細。
type FrameDetail struct {
	ID      uint64          `json:"id"`
	Dir     string          `json:"dir"`
	T       float64         `json:"t"`
	TRx     float64         `json:"trx,omitempty"`
	Len     int             `json:"len"`
	Label   string          `json:"label"`
	Summary string          `json:"summary"`
	Retx    bool            `json:"retx,omitempty"`
	Drop    bool            `json:"drop,omitempty"`
	Reason  string          `json:"reason,omitempty"`
	Layers  []mininet.Layer `json:"layers"`
	Hex     string          `json:"hex"`
}

func (s *Session) Frame(id uint64) (FrameDetail, bool) {
	s.mu.Lock()
	if id == 0 || int(id) > len(s.frames) {
		s.mu.Unlock()
		return FrameDetail{}, false
	}
	r := s.frames[id-1]
	f := r.f
	d := FrameDetail{ID: f.ID, Dir: dirName(f.Dir), T: s.ms(f.SentAt), Len: len(f.Data), Label: r.label,
		Summary: f.Summary, Retx: r.retx, Drop: r.dropped, Reason: r.reason}
	if !r.rxAt.IsZero() && !r.dropped {
		d.TRx = s.ms(r.rxAt)
	}
	data := f.Data
	s.mu.Unlock()
	d.Layers = mininet.Decode(data)
	d.Hex = hex.EncodeToString(data)
	return d, true
}

// PCAP はセッションの全フレームを pcap にして返す。includeDropped=false なら届いたものだけ。
// 時刻は送信時刻 (A, B どちらのケーブル端で記録したものでもない「ケーブル上の観測」)。
func (s *Session) PCAP(includeDropped bool) []byte {
	var buf bytes.Buffer
	pw, _ := mininet.NewPcapWriterTo(&buf)
	s.mu.Lock()
	for _, r := range s.frames {
		if r.dropped && !includeDropped {
			continue
		}
		pw.WritePacketAt(r.f.SentAt, r.f.Data)
	}
	s.mu.Unlock()
	return buf.Bytes()
}

func dirName(d int) string {
	if d == mininet.DirAtoB {
		return "AB"
	}
	return "BA"
}

// MarshalJSON 用の小さなヘルパ。
func JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}
