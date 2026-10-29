package main

// link.go — L2 (データリンク層) の抽象化。
//
// スタック本体は「Ethernet フレームを送る/受け取る」ことしか要求しない。
// 実機の NIC や TAP デバイスの代わりに、ここでは次の 2 種類の「仮想ケーブル」を用意する。
//
//   - ChanWire : 同一プロセス内で Go の channel をケーブル代わりにする (テスト用)
//   - UDPWire  : Ethernet フレームを丸ごと UDP データグラムに詰めて localhost で運ぶ
//                (プロセス間をつなぐ。VDE や QEMU の -netdev socket と同じ発想)
//
// LossyLink は任意の LinkDevice を包んで、一定確率でフレームを捨てる (再送のテスト用)。

import (
	"errors"
	"math/rand"
	"net"
	"sync"
)

// LinkDevice は Ethernet フレームを送受信できるもの。
type LinkDevice interface {
	Send(frame []byte) error // 1 フレーム送信 (ブロックしない)
	Recv() ([]byte, error)   // 1 フレーム受信 (届くまでブロック)
	Close() error
}

var ErrLinkClosed = errors.New("link closed")

// ---------------------------------------------------------------------------
// ChanWire: channel 2 本で作る全二重ケーブル

type chanEnd struct {
	tx     chan []byte
	rx     chan []byte
	closed chan struct{}
	once   *sync.Once
}

// NewChanWire はケーブルの両端 a, b を返す。a.Send したものは b.Recv で出てくる。
func NewChanWire() (LinkDevice, LinkDevice) {
	ab := make(chan []byte, 4096)
	ba := make(chan []byte, 4096)
	closed := make(chan struct{})
	once := &sync.Once{}
	return &chanEnd{tx: ab, rx: ba, closed: closed, once: once},
		&chanEnd{tx: ba, rx: ab, closed: closed, once: once}
}

func (c *chanEnd) Send(frame []byte) error {
	f := append([]byte(nil), frame...) // 送信側バッファの再利用に備えてコピー
	select {
	case <-c.closed:
		return ErrLinkClosed
	case c.tx <- f:
	default:
		// キューが溢れたら本物の NIC と同じく黙って捨てる (ブロックするとデッドロックの元)
	}
	return nil
}

func (c *chanEnd) Recv() ([]byte, error) {
	select {
	case <-c.closed:
		return nil, ErrLinkClosed
	case f := <-c.rx:
		return f, nil
	}
}

func (c *chanEnd) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// ---------------------------------------------------------------------------
// LossyLink: 送信フレームを確率 p で捨てる

type LossyLink struct {
	LinkDevice
	mu      sync.Mutex
	rnd     *rand.Rand
	p       float64
	Dropped int
}

func NewLossyLink(dev LinkDevice, p float64, seed int64) *LossyLink {
	return &LossyLink{LinkDevice: dev, p: p, rnd: rand.New(rand.NewSource(seed))}
}

func (l *LossyLink) Send(frame []byte) error {
	l.mu.Lock()
	drop := l.rnd.Float64() < l.p
	if drop {
		l.Dropped++
	}
	l.mu.Unlock()
	if drop {
		return nil
	}
	return l.LinkDevice.Send(frame)
}

// ---------------------------------------------------------------------------
// UDPWire: Ethernet フレーム 1 つ = UDP データグラム 1 つ
//
//   [プロセス A: mininet] --UDP 127.0.0.1:9001 <-> 127.0.0.1:9002--> [プロセス B: mininet]
//
// ホスト OS から見るとただの UDP 通信なので root 権限は不要。

type UDPWire struct {
	conn *net.UDPConn
	peer *net.UDPAddr
}

func NewUDPWire(listen, peer string) (*UDPWire, error) {
	la, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return nil, err
	}
	pa, err := net.ResolveUDPAddr("udp", peer)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", la)
	if err != nil {
		return nil, err
	}
	conn.SetReadBuffer(4 << 20)
	conn.SetWriteBuffer(4 << 20)
	return &UDPWire{conn: conn, peer: pa}, nil
}

func (w *UDPWire) Send(frame []byte) error {
	_, err := w.conn.WriteToUDP(frame, w.peer)
	if err != nil {
		// 相手プロセスがまだ起動していない (ICMP port unreachable) 等。ケーブルが抜けているのと同じ扱い。
		return nil
	}
	return nil
}

func (w *UDPWire) Recv() ([]byte, error) {
	buf := make([]byte, 65536)
	for {
		n, _, err := w.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil, ErrLinkClosed
			}
			continue // macOS では相手不在時に ECONNREFUSED が返ることがある
		}
		return append([]byte(nil), buf[:n]...), nil
	}
}

func (w *UDPWire) Close() error { return w.conn.Close() }
