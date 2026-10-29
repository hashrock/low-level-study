package mininet

// snapshot.go — スタックの内部状態を読み出す (可視化・デバッグ用)。
//
// ロックの順序: TCPConn.mu → Stack.mu の順で取る箇所 (closeLocked) があるので、
// ここでは Stack.mu の下でコネクション一覧をコピーしてから、1 つずつ TCPConn.mu を取る。

import (
	"fmt"
	"sort"
	"time"
)

// TCPConnID はコネクションを表す 4 つ組。
type TCPConnID struct {
	LocalIP    IP
	LocalPort  uint16
	RemoteIP   IP
	RemotePort uint16
}

func (id TCPConnID) String() string {
	return fmt.Sprintf("%v:%d<->%v:%d", id.LocalIP, id.LocalPort, id.RemoteIP, id.RemotePort)
}

func (c *TCPConn) id() TCPConnID {
	return TCPConnID{LocalIP: c.s.IP, LocalPort: c.key.localPort, RemoteIP: c.key.remoteIP, RemotePort: c.key.remotePort}
}

// ID はこのコネクションの 4 つ組を返す。
func (c *TCPConn) ID() TCPConnID { return c.id() }

// announce は「コネクションが生まれた」遷移 (CLOSED→SYN_SENT, LISTEN→SYN_RCVD) をフックに知らせる。
func (c *TCPConn) announce(from TCPState) {
	if h := c.s.OnTCPState; h != nil {
		h(c.id(), from, c.state)
	}
}

// TCPConnSnapshot は 1 本のコネクションの状態。シーケンス番号は生の値 (相対値は ISS/IRS を引けば得られる)。
type TCPConnSnapshot struct {
	ID         TCPConnID `json:"-"`
	LocalPort  uint16    `json:"lport"`
	RemoteIP   string    `json:"rip"`
	RemotePort uint16    `json:"rport"`
	State      string    `json:"state"`
	ISS        uint32    `json:"iss"`
	IRS        uint32    `json:"irs"`
	SndUna     uint32    `json:"sndUna"`
	SndNxt     uint32    `json:"sndNxt"`
	SndMax     uint32    `json:"sndMax"`
	SndWnd     uint16    `json:"sndWnd"` // 相手が広告した受信ウィンドウ
	RcvNxt     uint32    `json:"rcvNxt"`
	RcvWnd     uint16    `json:"rcvWnd"`   // 自分の受信ウィンドウ (受信バッファの空き)
	Unacked    int       `json:"unacked"`  // 送信済み未 ACK のバイト数 (= 再送キュー)
	Unsent     int       `json:"unsent"`   // 送信バッファにあってまだ送っていないバイト数
	RetxQueue  int       `json:"retxSegs"` // 未 ACK のセグメント数 (MSS 換算の目安)
	RecvBuf    int       `json:"recvBuf"`  // アプリがまだ Read していないバイト数
	RTOms      float64   `json:"rto"`
	Retries    int       `json:"retries"`
	TimerOn    bool      `json:"timer"`
	DupAcks    int       `json:"dupAcks"`
	FinSent    bool      `json:"finSent"`
	PeerFin    bool      `json:"peerFin"`
}

func (c *TCPConn) Snapshot() TCPConnSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	unacked := int(c.sndNxt - c.sndUna)
	if c.state == StateSynSent || c.state == StateSynRcvd {
		unacked = 0
	} else if c.finSent && !c.finAcked {
		unacked-- // FIN の 1 バイトぶん
	}
	unacked = max(0, min(unacked, len(c.sendBuf)))
	outstanding := int(c.sndNxt - c.sndUna)
	return TCPConnSnapshot{
		ID:        c.id(),
		LocalPort: c.key.localPort, RemoteIP: c.key.remoteIP.String(), RemotePort: c.key.remotePort,
		State: c.state.String(),
		ISS:   c.iss, IRS: c.irs,
		SndUna: c.sndUna, SndNxt: c.sndNxt, SndMax: c.sndMax, SndWnd: c.sndWnd,
		RcvNxt: c.rcvNxt, RcvWnd: c.advWindow(),
		Unacked: unacked, Unsent: len(c.sendBuf) - unacked,
		RetxQueue: (max(outstanding, 0) + tcpMSS - 1) / tcpMSS,
		RecvBuf:   len(c.recvBuf),
		RTOms:     float64(c.rto) / float64(time.Millisecond),
		Retries:   c.retries, TimerOn: c.timerOn, DupAcks: c.dupAcks,
		FinSent: c.finSent, PeerFin: c.peerFin,
	}
}

// ARPEntry は ARP キャッシュの 1 行。
type ARPEntry struct {
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
	Pending int    `json:"pending,omitempty"` // MAC 解決待ちの IP パケット数 (MAC 未解決の行)
}

// StackSnapshot はスタック全体の状態。
type StackSnapshot struct {
	IP        string            `json:"ip"`
	MAC       string            `json:"mac"`
	ARP       []ARPEntry        `json:"arp"`
	TCP       []TCPConnSnapshot `json:"tcp"`
	Listeners []uint16          `json:"listen"`
	UDP       []uint16          `json:"udp"`
}

func (s *Stack) ARPEntries() []ARPEntry {
	t := s.arp
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []ARPEntry
	for ip, mac := range t.cache {
		out = append(out, ARPEntry{IP: ip.String(), MAC: mac.String()})
	}
	for ip, q := range t.pending {
		if _, ok := t.cache[ip]; !ok {
			out = append(out, ARPEntry{IP: ip.String(), MAC: "(incomplete)", Pending: len(q)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// FlushARP は ARP キャッシュを空にする (次の送信で再び who-has が飛ぶ)。
func (s *Stack) FlushARP() {
	t := s.arp
	t.mu.Lock()
	clear(t.cache)
	clear(t.lastReq)
	t.mu.Unlock()
}

// TCPConns は現在のコネクション一覧を返す (ポート順)。
func (s *Stack) TCPConns() []*TCPConn {
	s.mu.Lock()
	conns := make([]*TCPConn, 0, len(s.tcpConns))
	for _, c := range s.tcpConns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	sort.Slice(conns, func(i, j int) bool {
		a, b := conns[i].key, conns[j].key
		if a.localPort != b.localPort {
			return a.localPort < b.localPort
		}
		return a.remotePort < b.remotePort
	})
	return conns
}

func (s *Stack) Snapshot() StackSnapshot {
	snap := StackSnapshot{IP: s.IP.String(), MAC: s.MAC.String(), ARP: s.ARPEntries()}
	for _, c := range s.TCPConns() {
		snap.TCP = append(snap.TCP, c.Snapshot())
	}
	s.mu.Lock()
	for p := range s.listeners {
		snap.Listeners = append(snap.Listeners, p)
	}
	for p := range s.udpSocks {
		snap.UDP = append(snap.UDP, p)
	}
	s.mu.Unlock()
	sort.Slice(snap.Listeners, func(i, j int) bool { return snap.Listeners[i] < snap.Listeners[j] })
	sort.Slice(snap.UDP, func(i, j int) bool { return snap.UDP[i] < snap.UDP[j] })
	return snap
}
