package main

// tcp_conn.go — TCP コネクションの状態機械と送受信バッファ、再送タイマ
//
// 送信側のシーケンス空間:
//
//        sndUna            sndNxt                     sndUna+len(sendBuf)
//   ------+-----------------+--------------------------+------
//    ACK済 | 送信済・未ACK    | 未送信 (ウィンドウが空き次第) |
//         |<---------- sendBuf ------------------------>|
//
//   SYN と FIN はそれぞれシーケンス番号を 1 つ消費する。
//
// 受信側: rcvNxt ちょうどから始まるセグメントだけ受け取る (順序外は捨てて ACK を返すだけ)。
// 受信ウィンドウ = 受信バッファの空き (最大 65535 bytes 固定)。
//
// 再送: 1 本のタイマ (RTO)。タイムアウトしたら sndNxt を sndUna まで巻き戻して
// そこから送り直す (Go-Back-N)。RTO は倍々に延ばし、新しい ACK が来たら初期値に戻す。
// 重複 ACK を 3 回受けたら、タイムアウトを待たずに同様に巻き戻す (高速再送)。
// 輻輳制御 (slow start 等) は実装していない。

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"
)

type TCPState int

const (
	StateClosed TCPState = iota
	StateListen
	StateSynSent
	StateSynRcvd
	StateEstablished
	StateFinWait1
	StateFinWait2
	StateCloseWait
	StateClosing
	StateLastAck
	StateTimeWait
)

var stateNames = [...]string{"CLOSED", "LISTEN", "SYN_SENT", "SYN_RCVD", "ESTABLISHED",
	"FIN_WAIT_1", "FIN_WAIT_2", "CLOSE_WAIT", "CLOSING", "LAST_ACK", "TIME_WAIT"}

func (s TCPState) String() string { return stateNames[s] }

const (
	tcpMSS         = 1460 // 1500 (MTU) - 20 (IP) - 20 (TCP)
	tcpRecvBufSize = 65535
	tcpSendBufSize = 256 * 1024
	tcpInitialRTO  = 200 * time.Millisecond // 本来は 1 秒 (RFC 6298)。ローカル実験向けに短め
	tcpMaxRTO      = 3 * time.Second
	tcpMaxRetries  = 12
)

var (
	ErrConnReset   = errors.New("connection reset by peer")
	ErrConnTimeout = errors.New("connection timed out")
)

type TCPConn struct {
	s        *Stack
	key      tcpKey
	listener *TCPListener

	mu    sync.Mutex
	cond  *sync.Cond
	state TCPState
	err   error

	// 送信
	iss       uint32 // 初期シーケンス番号
	sndUna    uint32 // まだ ACK されていない最古のバイト
	sndNxt    uint32 // 次に送るバイト
	sndMax    uint32 // これまでに送った最大 sndNxt (巻き戻し後の ACK 判定に使う)
	sndWnd    uint16 // 相手の受信ウィンドウ
	sendBuf   []byte // sndUna 以降のデータ
	finQueued bool   // アプリが Close した
	finSent   bool
	finAcked  bool
	dupAcks   int
	fastRetx  uint32 // 同じ sndUna で高速再送を繰り返さないため

	// 受信
	irs        uint32
	rcvNxt     uint32 // 次に期待するシーケンス番号
	recvBuf    []byte // アプリがまだ Read していないデータ
	peerFin    bool
	lastAdvWnd uint16

	// タイマ
	rto      time.Duration
	retries  int
	timer    *time.Timer
	timerGen int
	timerOn  bool
}

func (s *Stack) newConn(key tcpKey, st TCPState) *TCPConn {
	c := &TCPConn{s: s, key: key, state: st, rto: tcpInitialRTO}
	c.cond = sync.NewCond(&c.mu)
	c.iss = rand.Uint32()
	c.sndUna, c.sndNxt, c.sndMax = c.iss, c.iss, c.iss
	return c
}

func (c *TCPConn) logf(format string, args ...any) {
	c.s.logf("tcp %d<->%v:%d "+format, append([]any{c.key.localPort, c.key.remoteIP, c.key.remotePort}, args...)...)
}

func (c *TCPConn) setState(st TCPState) {
	c.logf("state %v -> %v", c.state, st)
	c.state = st
	c.cond.Broadcast()
}

func (c *TCPConn) State() TCPState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *TCPConn) RemoteAddr() string { return fmt.Sprintf("%v:%d", c.key.remoteIP, c.key.remotePort) }

// ---------------------------------------------------------------------------
// 公開 API: Listen / Accept / Dial / Read / Write / Close

type TCPListener struct {
	s        *Stack
	port     uint16
	acceptCh chan *TCPConn
	closed   chan struct{}
	once     sync.Once
}

func (s *Stack) ListenTCP(port uint16) (*TCPListener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.listeners[port]; ok {
		return nil, fmt.Errorf("tcp: port %d already listening", port)
	}
	l := &TCPListener{s: s, port: port, acceptCh: make(chan *TCPConn, 16), closed: make(chan struct{})}
	s.listeners[port] = l
	return l, nil
}

// Accept は 3-way handshake を終えた (ESTABLISHED になった) コネクションを返す。
func (l *TCPListener) Accept() (*TCPConn, error) {
	select {
	case c := <-l.acceptCh:
		return c, nil
	case <-l.closed:
		return nil, ErrClosed
	}
}

func (l *TCPListener) Close() error {
	l.once.Do(func() {
		l.s.mu.Lock()
		delete(l.s.listeners, l.port)
		l.s.mu.Unlock()
		close(l.closed)
	})
	return nil
}

// DialTCP は SYN を送り、ESTABLISHED になるまで待つ (active open)。
func (s *Stack) DialTCP(dst IP, port uint16) (*TCPConn, error) {
	s.mu.Lock()
	key := tcpKey{localPort: s.allocPortLocked(), remoteIP: dst, remotePort: port}
	c := s.newConn(key, StateSynSent)
	s.tcpConns[key] = c
	s.mu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.logf("state CLOSED -> SYN_SENT (dial)")
	c.output()
	for c.state == StateSynSent {
		c.cond.Wait()
	}
	if c.err != nil {
		return nil, c.err
	}
	return c, nil
}

// Read は受信済みデータを読む。相手が FIN を送ってきて、データを読み切ったら io.EOF。
func (c *TCPConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.recvBuf) == 0 && !c.peerFin && c.err == nil && c.state != StateClosed {
		c.cond.Wait()
	}
	if len(c.recvBuf) > 0 {
		n := copy(b, c.recvBuf)
		c.recvBuf = c.recvBuf[n:]
		// ウィンドウが大きく開いたら相手に知らせる (window update)
		if c.lastAdvWnd < tcpRecvBufSize/2 && c.advWindow() >= tcpRecvBufSize/2 {
			c.sendACK()
		}
		return n, nil
	}
	if c.err != nil {
		return 0, c.err
	}
	return 0, io.EOF
}

// Write は送信バッファにデータを積んで送信を試みる。バッファが一杯ならブロックする。
func (c *TCPConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for len(b) > 0 {
		for c.err == nil && c.canWrite() && len(c.sendBuf) >= tcpSendBufSize {
			c.cond.Wait()
		}
		if c.err != nil {
			return total, c.err
		}
		if !c.canWrite() {
			return total, ErrClosed
		}
		n := min(len(b), tcpSendBufSize-len(c.sendBuf))
		c.sendBuf = append(c.sendBuf, b[:n]...)
		b = b[n:]
		total += n
		c.output()
	}
	return total, nil
}

func (c *TCPConn) canWrite() bool {
	return !c.finQueued && (c.state == StateEstablished || c.state == StateCloseWait)
}

// Close は FIN を送る (未送信データを送り終えてから)。ブロックしない。
// 送信方向だけを閉じるので、相手の FIN が来るまでは Read を続けられる (half-close)。
func (c *TCPConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case StateEstablished:
		c.finQueued = true
		c.setState(StateFinWait1)
	case StateCloseWait:
		c.finQueued = true
		c.setState(StateLastAck)
	case StateSynSent, StateSynRcvd:
		c.closeLocked(nil)
	default:
		return nil
	}
	c.output()
	return nil
}

// WaitClosed は CLOSED になるまで待つ (テスト用)。
func (c *TCPConn) WaitClosed(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.State() == StateClosed {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// 送信処理

func (c *TCPConn) advWindow() uint16 {
	return uint16(tcpRecvBufSize - len(c.recvBuf))
}

func (c *TCPConn) sendSegment(flags uint8, seq uint32, payload []byte) {
	seg := TCPSegment{
		SrcPort: c.key.localPort, DstPort: c.key.remotePort,
		Seq: seq, Flags: flags, Window: c.advWindow(), Payload: payload,
	}
	if flags&tcpACK != 0 {
		seg.Ack = c.rcvNxt
	}
	c.lastAdvWnd = seg.Window
	c.logf("tx %v", seg)
	c.s.SendIP(c.key.remoteIP, ProtoTCP, seg.Marshal(c.s.IP, c.key.remoteIP))
}

func (c *TCPConn) sendACK() { c.sendSegment(tcpACK, c.sndNxt, nil) }

// output は今の状態で送れるものをすべて送り、必要なら再送タイマを起動する。
func (c *TCPConn) output() {
	switch c.state {
	case StateSynSent:
		if c.sndNxt == c.iss {
			c.sendSegment(tcpSYN, c.iss, nil)
			c.sndNxt = c.iss + 1
		}
	case StateSynRcvd:
		if c.sndNxt == c.iss {
			c.sendSegment(tcpSYN|tcpACK, c.iss, nil)
			c.sndNxt = c.iss + 1
		}
	case StateEstablished, StateCloseWait, StateFinWait1, StateClosing, StateLastAck:
		for {
			off := int(c.sndNxt - c.sndUna) // = 送信済み未 ACK のバイト数
			if off >= len(c.sendBuf) {
				break
			}
			room := int(c.sndWnd) - off // 相手のウィンドウの残り
			if room <= 0 {
				break
			}
			n := min(tcpMSS, len(c.sendBuf)-off, room)
			c.sendSegment(tcpACK|tcpPSH, c.sndNxt, c.sendBuf[off:off+n])
			c.sndNxt += uint32(n)
		}
		if c.finQueued && !c.finSent && !c.finAcked && int(c.sndNxt-c.sndUna) == len(c.sendBuf) {
			c.sendSegment(tcpFIN|tcpACK, c.sndNxt, nil)
			c.sndNxt++
			c.finSent = true
		}
	default:
		return
	}
	if seqLT(c.sndMax, c.sndNxt) {
		c.sndMax = c.sndNxt
	}
	if !c.timerOn && (c.sndNxt != c.sndUna || len(c.sendBuf) > 0) {
		// 未 ACK のものがある → 再送タイマ。何も送れていないがデータがある (相手のウィンドウ 0) → 持続タイマ代わり
		c.armTimer(c.rto)
	}
}

func (c *TCPConn) armTimer(d time.Duration) {
	c.stopTimer()
	gen := c.timerGen
	c.timer = time.AfterFunc(d, func() { c.onTimer(gen) })
	c.timerOn = true
}

func (c *TCPConn) stopTimer() {
	c.timerGen++ // 既に発火して lock 待ちのコールバックを無効化する
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timerOn = false
}

func (c *TCPConn) onTimer(gen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.timerGen {
		return
	}
	c.timerOn = false
	switch c.state {
	case StateClosed:
		return
	case StateTimeWait:
		c.closeLocked(nil)
		return
	}
	if c.sndNxt == c.sndUna && c.sndWnd == 0 && len(c.sendBuf) > 0 {
		// ゼロウィンドウ・プローブ: 1 バイトだけ送って相手のウィンドウを聞き出す
		c.logf("zero window probe")
		c.sendSegment(tcpACK, c.sndNxt, c.sendBuf[:1])
		c.sndNxt++
		c.armTimer(c.rto)
		return
	}
	if c.sndWnd != 0 {
		c.retries++
	}
	if c.retries > tcpMaxRetries {
		c.logf("too many retransmissions, giving up")
		c.closeLocked(ErrConnTimeout)
		return
	}
	c.rto = min(c.rto*2, tcpMaxRTO)
	c.logf("RTO expired: retransmit from seq=%d (rto now %v)", c.sndUna, c.rto)
	c.rewind()
	c.output()
}

// rewind は Go-Back-N: 未 ACK の先頭から送り直す準備をする。
func (c *TCPConn) rewind() {
	if c.state == StateSynSent || c.state == StateSynRcvd {
		c.sndNxt = c.iss
		return
	}
	c.sndNxt = c.sndUna
	if !c.finAcked {
		c.finSent = false
	}
}

func (c *TCPConn) closeLocked(err error) {
	if c.state != StateClosed {
		c.setState(StateClosed)
	}
	if c.err == nil {
		c.err = err
	}
	c.stopTimer()
	c.s.mu.Lock()
	if c.s.tcpConns[c.key] == c {
		delete(c.s.tcpConns, c.key)
	}
	c.s.mu.Unlock()
	c.cond.Broadcast()
}

func (c *TCPConn) enterTimeWait() {
	c.setState(StateTimeWait)
	c.armTimer(c.s.TimeWait)
}

// ---------------------------------------------------------------------------
// 受信処理 (RFC 793 "SEGMENT ARRIVES" を大幅に簡略化したもの)

func (c *TCPConn) handleSegment(seg TCPSegment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logf("rx %v", seg)

	// RST: 即座にコネクションを破棄
	if seg.Flags&tcpRST != 0 {
		if c.state == StateSynSent && (seg.Flags&tcpACK == 0 || seg.Ack != c.iss+1) {
			return // 自分の SYN に対するものではない
		}
		c.closeLocked(ErrConnReset)
		return
	}

	// SYN_SENT: SYN+ACK を待っている
	if c.state == StateSynSent {
		if seg.Flags&(tcpSYN|tcpACK) == tcpSYN|tcpACK && seg.Ack == c.iss+1 {
			c.irs = seg.Seq
			c.rcvNxt = seg.Seq + 1
			c.sndUna = seg.Ack
			c.sndWnd = seg.Window
			c.stopTimer()
			c.rto, c.retries = tcpInitialRTO, 0
			c.setState(StateEstablished)
			c.sendACK() // 3-way handshake の 3 つ目
		}
		return
	}

	// 重複した SYN (相手が SYN を再送してきた = こちらの SYN+ACK か ACK が失われた)
	if seg.Flags&tcpSYN != 0 {
		if c.state == StateSynRcvd {
			c.sndNxt = c.iss
			c.output()
		} else {
			c.sendACK()
		}
		return
	}
	if seg.Flags&tcpACK == 0 {
		return // SYN 以外で ACK が立っていないセグメントは無視
	}

	// ---- ACK の処理 ----
	if c.state == StateSynRcvd {
		if seg.Ack != c.iss+1 {
			return
		}
		c.sndUna = seg.Ack
		c.sndWnd = seg.Window
		c.stopTimer()
		c.rto, c.retries = tcpInitialRTO, 0
		c.setState(StateEstablished)
		select {
		case c.listener.acceptCh <- c:
		default:
			c.logf("accept queue full")
		}
	}
	if seqLT(c.sndUna, seg.Ack) && seqLE(seg.Ack, c.sndMax) {
		// 新しいデータが ACK された
		acked := int(seg.Ack - c.sndUna)
		if acked > len(c.sendBuf) { // データ全部 + FIN の 1 バイト
			c.finAcked = true
			acked = len(c.sendBuf)
		}
		c.sendBuf = c.sendBuf[acked:]
		c.sndUna = seg.Ack
		if seqLT(c.sndNxt, c.sndUna) {
			c.sndNxt = c.sndUna
		}
		c.rto, c.retries, c.dupAcks = tcpInitialRTO, 0, 0
		c.stopTimer()
		c.cond.Broadcast() // Write で待っている人を起こす
		if c.finAcked {
			switch c.state {
			case StateFinWait1:
				c.setState(StateFinWait2)
			case StateClosing:
				c.enterTimeWait()
			case StateLastAck:
				c.closeLocked(nil)
				return
			}
		}
	} else if seg.Ack == c.sndUna && len(seg.Payload) == 0 && seg.Flags&tcpFIN == 0 &&
		c.sndNxt != c.sndUna && seg.Window == c.sndWnd {
		// 重複 ACK: 相手は何か (sndUna のセグメント) を受け取れていない
		c.dupAcks++
		if c.dupAcks == 3 && c.fastRetx != c.sndUna {
			c.fastRetx = c.sndUna
			c.logf("3 duplicate ACKs: fast retransmit from seq=%d", c.sndUna)
			c.rewind()
		}
	}
	if seqLE(c.sndUna, seg.Ack) {
		c.sndWnd = seg.Window
	}

	// ---- データの受信 ----
	needACK := false
	if len(seg.Payload) > 0 {
		needACK = true
		if c.state == StateEstablished || c.state == StateFinWait1 || c.state == StateFinWait2 {
			data, seq := seg.Payload, seg.Seq
			if seqLT(seq, c.rcvNxt) { // 一部 (または全部) が受信済み → 重複部分を切り捨てる
				skip := int(c.rcvNxt - seq)
				if skip >= len(data) {
					data = nil
				} else {
					data = data[skip:]
				}
				seq = c.rcvNxt
			}
			if seq == c.rcvNxt && len(data) > 0 {
				n := min(len(data), tcpRecvBufSize-len(c.recvBuf)) // ウィンドウ外は捨てる
				c.recvBuf = append(c.recvBuf, data[:n]...)
				c.rcvNxt += uint32(n)
				c.cond.Broadcast()
			}
			// seq > rcvNxt (順序外) は捨てる。下で送る ACK が「重複 ACK」になり、相手の再送を促す
		}
	}

	// ---- FIN の受信 ----
	if seg.Flags&tcpFIN != 0 {
		needACK = true
		if seg.Seq+uint32(len(seg.Payload)) == c.rcvNxt && !c.peerFin { // FIN の前のデータを全部受け取った
			c.rcvNxt++
			c.peerFin = true
			c.cond.Broadcast()
			switch c.state {
			case StateEstablished:
				c.setState(StateCloseWait)
			case StateFinWait1: // 同時クローズ: こちらの FIN はまだ ACK されていない
				c.setState(StateClosing)
			case StateFinWait2:
				c.enterTimeWait()
			}
		} else if c.state == StateTimeWait {
			c.armTimer(c.s.TimeWait) // FIN の再送 = こちらの ACK が失われた。ACK し直してタイマを再スタート
		}
	}
	if needACK {
		c.sendACK()
	}
	c.output()
}
