package mininet

// simwire.go — 観察・操作できる仮想ケーブル (ブラウザでの可視化用)
//
// ChanWire と同じく同一プロセス内の 2 つのスタックをつなぐが、次の機能を持つ:
//
//   - 遅延 (latency): 送ったフレームは latency 後に相手に届く。順序は保つ (FIFO)
//   - 損失 (loss): 確率 p で捨てる。DropNext で「次の 1 フレームだけ捨てる」もできる
//   - 一時停止 (Pause): 停止中は配送しない (ケーブル上で止まる)。Step で 1 つだけ配送、
//     DropHead で先頭の 1 つを捨てる。再開すると停止していた時間だけ到着予定がずれる
//   - タップ (OnSend / OnDeliver / OnDrop): 全フレームを観察する
//
//        A.Send ──▶ [OnSend] ──(捨てる?)──▶ inflight キュー ──latency──▶ [OnDeliver] ──▶ B.Recv
//                                             └─ DropHead ─▶ [OnDrop]

import (
	"math/rand"
	"sync"
	"time"
)

// 方向
const (
	DirAtoB = 0
	DirBtoA = 1
)

// WireFrame はケーブルを流れた 1 フレーム。
type WireFrame struct {
	ID          uint64
	Dir         int // DirAtoB / DirBtoA
	Data        []byte
	Summary     string // 1 行の要約 (Summarize)
	SentAt      time.Time
	DeliveredAt time.Time // 届いた時刻 (届いていなければゼロ値)
	Dropped     bool
	DropReason  string // "loss" / "manual" / "held" / "queue"
	due         time.Time
}

type SimWire struct {
	// タップ。ワイヤのロックの外で呼ばれる。NewSimWire の直後、フレームが流れる前に設定すること。
	OnSend    func(f *WireFrame) // 送信時 (捨てられたフレームも Dropped=true で来る)
	OnDeliver func(f *WireFrame) // 相手に届いた時
	OnDrop    func(f *WireFrame) // ケーブル上で捨てられた時 (DropHead, 受信キューあふれ)

	// tapMu はタップの呼び出しを直列化し、観察者から見た順序 (send → deliver → 返信の send) を保証する
	tapMu    sync.Mutex
	mu       sync.Mutex
	rnd      *rand.Rand
	loss     float64
	latency  time.Duration
	dropNext bool
	paused   bool
	pausedAt time.Time
	nextID   uint64
	inflight []*WireFrame // 到着予定順 (= 送信順)
	lastDue  time.Time
	ends     [2]*simEnd
	wake     chan struct{}
	closed   chan struct{}
	once     sync.Once
}

type simEnd struct {
	w   *SimWire
	dir int // この端から送ったフレームの向き
	rx  chan []byte
}

// NewSimWire はケーブルを作り、両端 (A 側, B 側) を返す。
func NewSimWire(latency time.Duration, seed int64) (*SimWire, LinkDevice, LinkDevice) {
	w := &SimWire{
		rnd: rand.New(rand.NewSource(seed)), latency: latency,
		wake: make(chan struct{}, 1), closed: make(chan struct{}),
	}
	w.ends[0] = &simEnd{w: w, dir: DirAtoB, rx: make(chan []byte, 4096)}
	w.ends[1] = &simEnd{w: w, dir: DirBtoA, rx: make(chan []byte, 4096)}
	go w.run()
	return w, w.ends[0], w.ends[1]
}

func (w *SimWire) SetLoss(p float64) {
	w.mu.Lock()
	w.loss = p
	w.mu.Unlock()
}

func (w *SimWire) SetLatency(d time.Duration) {
	w.mu.Lock()
	w.latency = d
	w.mu.Unlock()
}

// DropNext は次に送られる 1 フレーム (どちら向きでも) を捨てる予約をする。
func (w *SimWire) DropNext() {
	w.mu.Lock()
	w.dropNext = true
	w.mu.Unlock()
}

func (w *SimWire) SetPaused(p bool) {
	w.mu.Lock()
	if p && !w.paused {
		w.paused, w.pausedAt = true, time.Now()
	} else if !p && w.paused {
		shift := time.Since(w.pausedAt)
		for _, f := range w.inflight {
			f.due = f.due.Add(shift)
		}
		w.lastDue = w.lastDue.Add(shift)
		w.paused = false
	}
	w.mu.Unlock()
	w.kick()
}

// Step は停止中のケーブルから先頭の 1 フレームを今すぐ届ける。届けたら true。
func (w *SimWire) Step() bool {
	w.mu.Lock()
	if len(w.inflight) == 0 {
		w.mu.Unlock()
		return false
	}
	f := w.inflight[0]
	w.inflight = w.inflight[1:]
	w.mu.Unlock()
	w.deliver(f)
	return true
}

// DropHead はケーブル上の先頭の 1 フレームを捨てる。捨てたら true。
func (w *SimWire) DropHead() bool {
	w.mu.Lock()
	if len(w.inflight) == 0 {
		w.mu.Unlock()
		return false
	}
	f := w.inflight[0]
	w.inflight = w.inflight[1:]
	f.Dropped, f.DropReason = true, "manual"
	w.mu.Unlock()
	if w.OnDrop != nil {
		w.tapMu.Lock()
		w.OnDrop(f)
		w.tapMu.Unlock()
	}
	return true
}

// WireState はケーブルの設定と状態。
type WireState struct {
	Loss      float64 `json:"loss"`
	LatencyMs float64 `json:"latency"`
	DropNext  bool    `json:"dropNext"`
	Paused    bool    `json:"paused"`
	InFlight  int     `json:"inflight"`
}

func (w *SimWire) State() WireState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WireState{Loss: w.loss, LatencyMs: float64(w.latency) / float64(time.Millisecond),
		DropNext: w.dropNext, Paused: w.paused, InFlight: len(w.inflight)}
}

func (w *SimWire) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

func (w *SimWire) kick() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *SimWire) send(dir int, frame []byte) error {
	select {
	case <-w.closed:
		return ErrLinkClosed
	default:
	}
	now := time.Now()
	f := &WireFrame{Dir: dir, Data: append([]byte(nil), frame...), SentAt: now}
	f.Summary = Summarize(f.Data)
	w.tapMu.Lock()
	defer w.tapMu.Unlock()
	w.mu.Lock()
	w.nextID++
	f.ID = w.nextID
	if w.dropNext {
		w.dropNext = false
		f.Dropped, f.DropReason = true, "manual"
	} else if w.loss > 0 && w.rnd.Float64() < w.loss {
		f.Dropped, f.DropReason = true, "loss"
	}
	if !f.Dropped {
		f.due = now.Add(w.latency)
		if f.due.Before(w.lastDue) { // 遅延を小さくしても追い越しは起こさない
			f.due = w.lastDue
		}
		w.lastDue = f.due
		w.inflight = append(w.inflight, f)
	}
	w.mu.Unlock()
	// OnSend を先に呼んでから配送ゴルーチンを起こす (観察者には必ず send → deliver の順で見える)
	if w.OnSend != nil {
		w.OnSend(f)
	}
	if !f.Dropped {
		w.kick()
	}
	return nil
}

func (w *SimWire) deliver(f *WireFrame) {
	w.tapMu.Lock()
	defer w.tapMu.Unlock()
	f.DeliveredAt = time.Now()
	select {
	case w.ends[1-f.Dir].rx <- f.Data:
		if w.OnDeliver != nil {
			w.OnDeliver(f)
		}
	default:
		f.Dropped, f.DropReason = true, "queue"
		if w.OnDrop != nil {
			w.OnDrop(f)
		}
	}
}

// run は配送ゴルーチン: 到着予定時刻になったフレームを相手の受信キューに入れる。
func (w *SimWire) run() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		w.mu.Lock()
		var wait time.Duration = -1
		var ready *WireFrame
		if !w.paused && len(w.inflight) > 0 {
			if d := time.Until(w.inflight[0].due); d <= 0 {
				ready = w.inflight[0]
				w.inflight = w.inflight[1:]
			} else {
				wait = d
			}
		}
		w.mu.Unlock()
		if ready != nil {
			w.deliver(ready)
			continue
		}
		var tc <-chan time.Time
		if wait >= 0 {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(wait)
			tc = timer.C
		}
		select {
		case <-w.closed:
			return
		case <-w.wake:
		case <-tc:
		}
	}
}

func (e *simEnd) Send(frame []byte) error { return e.w.send(e.dir, frame) }

func (e *simEnd) Recv() ([]byte, error) {
	select {
	case <-e.w.closed:
		return nil, ErrLinkClosed
	case f := <-e.rx:
		return f, nil
	}
}

func (e *simEnd) Close() error { return e.w.Close() }
