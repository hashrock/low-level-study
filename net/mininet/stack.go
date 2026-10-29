package mininet

// stack.go — プロトコルスタック本体: 受信ループと層間の振り分け
//
//   アプリ        Ping()   UDPConn      TCPConn / TCPListener
//                   |        |               |
//   L4            icmp.go  udp.go        tcp.go, tcp_conn.go
//                   \        |              /
//   L3                    SendIP / handleIPv4   (ipv4.go)
//                            |
//   L2.5                  resolveAndSend        (arp.go)
//                            |
//   L2                    sendFrame / handleFrame (ethernet.go)
//                            |
//   L1(仮想)               LinkDevice            (link.go)  ── pcap.go へコピー
//
// ルーティング: 宛先はすべて同一リンク上にいるとみなす (デフォルトゲートウェイなし)。

import (
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

type Stack struct {
	MAC MAC
	IP  IP

	dev  LinkDevice
	pcap *PcapWriter
	arp  *arpTable

	// Logf が非 nil なら各層のイベントを表示する (学習用)
	Logf func(format string, args ...any)
	// TIME_WAIT の長さ。本来は 2*MSL (数十秒〜数分) だが、実験しやすいよう短くしている
	TimeWait time.Duration
	// OnTCPState が非 nil なら TCP の状態遷移ごとに呼ばれる (可視化用)。
	// コネクションのロックを持ったまま呼ばれるので、中で TCPConn のメソッドを呼ばないこと。
	OnTCPState func(info TCPConnID, from, to TCPState)

	// 再送タイマの初期値と上限 (ナノ秒, atomic)。SetRTO で変更できる
	initialRTO atomic.Int64
	maxRTO     atomic.Int64

	mu          sync.Mutex
	ipID        uint16
	pingID      uint16
	pingWaiters map[uint32]chan struct{}
	udpSocks    map[uint16]*UDPConn
	tcpConns    map[tcpKey]*TCPConn
	listeners   map[uint16]*TCPListener
	nextPort    uint16

	done chan struct{}
}

// NewStack はスタックを作り、受信ゴルーチンを起動する。pcap は nil 可。
func NewStack(dev LinkDevice, mac MAC, ip IP, pcap *PcapWriter) *Stack {
	s := &Stack{
		MAC: mac, IP: ip, dev: dev, pcap: pcap, arp: newARPTable(),
		TimeWait:    time.Second,
		ipID:        uint16(rand.Intn(65536)),
		pingID:      uint16(rand.Intn(65536)),
		pingWaiters: map[uint32]chan struct{}{},
		udpSocks:    map[uint16]*UDPConn{},
		tcpConns:    map[tcpKey]*TCPConn{},
		listeners:   map[uint16]*TCPListener{},
		nextPort:    uint16(49152 + rand.Intn(16000)),
		done:        make(chan struct{}),
	}
	s.SetRTO(tcpInitialRTO, tcpMaxRTO)
	go s.rxLoop()
	return s
}

// Close はリンクを閉じ、受信ゴルーチンの終了を待つ。
// 残っている TCP コネクションは ErrClosed で打ち切り、待っている Read/Dial を起こす。
func (s *Stack) Close() error {
	err := s.dev.Close()
	<-s.done
	s.mu.Lock()
	conns := make([]*TCPConn, 0, len(s.tcpConns))
	for _, c := range s.tcpConns {
		conns = append(conns, c)
	}
	ls := make([]*TCPListener, 0, len(s.listeners))
	for _, l := range s.listeners {
		ls = append(ls, l)
	}
	us := make([]*UDPConn, 0, len(s.udpSocks))
	for _, u := range s.udpSocks {
		us = append(us, u)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		c.closeLocked(ErrClosed)
		c.mu.Unlock()
	}
	for _, l := range ls {
		l.Close()
	}
	for _, u := range us {
		u.Close()
	}
	return err
}

// SetRTO は再送タイマの初期値と上限を変える (遅延の大きいリンク用)。既存のコネクションにも次の ACK から効く。
func (s *Stack) SetRTO(initial, max time.Duration) {
	s.initialRTO.Store(int64(initial))
	s.maxRTO.Store(int64(max))
}

func (s *Stack) rtoInit() time.Duration { return time.Duration(s.initialRTO.Load()) }
func (s *Stack) rtoMax() time.Duration  { return time.Duration(s.maxRTO.Load()) }

func (s *Stack) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf("[%v] "+format, append([]any{s.IP}, args...)...)
	}
}

// StdLogf は Logf に設定できる標準のロガー。
func StdLogf(format string, args ...any) { log.Printf(format, args...) }

func (s *Stack) rxLoop() {
	defer close(s.done)
	for {
		frame, err := s.dev.Recv()
		if err != nil {
			return
		}
		s.pcap.WritePacket(frame)
		s.handleFrame(frame)
	}
}

// ---- L2 ----

func (s *Stack) handleFrame(frame []byte) {
	eth, payload, err := ParseEthernet(frame)
	if err != nil {
		return
	}
	if eth.Dst != s.MAC && eth.Dst != BroadcastMAC {
		return // 自分宛てでもブロードキャストでもない (プロミスキャスモードではない)
	}
	switch eth.Type {
	case EtherTypeARP:
		s.handleARP(payload)
	case EtherTypeIPv4:
		s.handleIPv4(payload)
	}
}

func (s *Stack) sendFrame(dst MAC, etherType uint16, payload []byte) {
	frame := EthernetHeader{Dst: dst, Src: s.MAC, Type: etherType}.Marshal(payload)
	s.pcap.WritePacket(frame)
	s.dev.Send(frame)
}

// ---- L3 ----

func (s *Stack) handleIPv4(b []byte) {
	h, payload, err := ParseIPv4(b)
	if err != nil {
		s.logf("ip: drop: %v", err)
		return
	}
	if h.Dst != s.IP {
		return // ルータではないので転送はしない
	}
	if h.FlagsOff&(ipFlagMF|0x1fff) != 0 {
		s.logf("ip: drop fragment from %v (reassembly not supported)", h.Src)
		return
	}
	switch h.Protocol {
	case ProtoICMP:
		s.handleICMP(h, payload)
	case ProtoUDP:
		s.handleUDP(h, payload)
	case ProtoTCP:
		s.handleTCP(h, payload)
	}
}

// SendIP は L4 のペイロードに IPv4 ヘッダを付けて送る。
func (s *Stack) SendIP(dst IP, proto uint8, payload []byte) error {
	if ipv4HeaderLen+len(payload) > 1500 {
		return fmt.Errorf("ip: packet too large (%d bytes, no fragmentation)", ipv4HeaderLen+len(payload))
	}
	s.mu.Lock()
	s.ipID++
	id := s.ipID
	s.mu.Unlock()
	h := IPv4Header{ID: id, FlagsOff: ipFlagDF, TTL: 64, Protocol: proto, Src: s.IP, Dst: dst}
	s.resolveAndSend(dst, h.Marshal(payload))
	return nil
}
