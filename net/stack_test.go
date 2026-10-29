package main

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	ipA  = IP{10, 0, 0, 1}
	ipB  = IP{10, 0, 0, 2}
	macA = MAC{0x02, 0, 0, 0, 0, 0x01}
	macB = MAC{0x02, 0, 0, 0, 0, 0x02}
)

// newPair は ChanWire でつながった 2 つのスタックを作る。loss > 0 なら両方向でフレームを落とす。
func newPair(t *testing.T, loss float64) (*Stack, *Stack) {
	t.Helper()
	var da, db LinkDevice
	da, db = NewChanWire()
	if loss > 0 {
		da = NewLossyLink(da, loss, 1)
		db = NewLossyLink(db, loss, 2)
	}
	a := NewStack(da, macA, ipA, nil)
	b := NewStack(db, macB, ipB, nil)
	a.TimeWait, b.TimeWait = 100*time.Millisecond, 100*time.Millisecond
	if os.Getenv("MININET_LOG") != "" {
		a.Logf, b.Logf = t.Logf, t.Logf
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func TestChecksum(t *testing.T) {
	// RFC 1071 の例
	b := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}
	if got := internetChecksum(b); got != ^uint16(0xddf2) {
		t.Fatalf("checksum = %#x", got)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	h := IPv4Header{ID: 7, TTL: 64, Protocol: ProtoTCP, Src: ipA, Dst: ipB}
	pkt := h.Marshal([]byte("hello"))
	h2, payload, err := ParseIPv4(pkt)
	if err != nil || h2.Src != ipA || h2.Dst != ipB || string(payload) != "hello" {
		t.Fatalf("ipv4 roundtrip: %v %+v %q", err, h2, payload)
	}
	pkt[8]-- // TTL を壊すとチェックサムエラーになるはず
	if _, _, err := ParseIPv4(pkt); err == nil {
		t.Fatal("expected checksum error")
	}
	seg := TCPSegment{SrcPort: 1, DstPort: 2, Seq: 100, Ack: 200, Flags: tcpSYN | tcpACK, Window: 999, Payload: []byte("x")}
	s2, err := ParseTCP(ipA, ipB, seg.Marshal(ipA, ipB))
	if err != nil || s2.Seq != 100 || s2.Ack != 200 || s2.Flags != tcpSYN|tcpACK || string(s2.Payload) != "x" {
		t.Fatalf("tcp roundtrip: %v %+v", err, s2)
	}
}

func TestARPResolution(t *testing.T) {
	a, b := newPair(t, 0)
	if _, err := a.Ping(ipB, 1, 8, time.Second); err != nil {
		t.Fatal(err)
	}
	if m, ok := a.ARPLookup(ipB); !ok || m != macB {
		t.Fatalf("A's ARP cache: %v %v", m, ok)
	}
	if m, ok := b.ARPLookup(ipA); !ok || m != macA {
		t.Fatalf("B's ARP cache (learned from request): %v %v", m, ok)
	}
}

func TestPing(t *testing.T) {
	a, _ := newPair(t, 0)
	for i := 0; i < 5; i++ {
		if _, err := a.Ping(ipB, uint16(i), 56, time.Second); err != nil {
			t.Fatalf("ping %d: %v", i, err)
		}
	}
	if _, err := a.Ping(IP{10, 0, 0, 99}, 1, 8, 300*time.Millisecond); err != ErrTimeout {
		t.Fatalf("ping to nonexistent host: %v", err)
	}
}

func TestUDPEcho(t *testing.T) {
	a, b := newPair(t, 0)
	srv, err := b.ListenUDP(7)
	if err != nil {
		t.Fatal(err)
	}
	go ServeUDPEcho(srv)
	cli, _ := a.ListenUDP(0)
	cli.WriteTo([]byte("hello udp"), ipB, 7)
	data, from, port, err := cli.ReadFrom(2 * time.Second)
	if err != nil || string(data) != "hello udp" || from != ipB || port != 7 {
		t.Fatalf("got %q from %v:%d err=%v", data, from, port, err)
	}
}

func TestTCPHandshakeAndClose(t *testing.T) {
	a, b := newPair(t, 0)
	l, _ := b.ListenTCP(80)
	accepted := make(chan *TCPConn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()

	c, err := a.DialTCP(ipB, 80)
	if err != nil {
		t.Fatal(err)
	}
	sc := <-accepted
	if c.State() != StateEstablished || sc.State() != StateEstablished {
		t.Fatalf("states: %v %v", c.State(), sc.State())
	}
	c.Write([]byte("ping"))
	buf := make([]byte, 10)
	n, _ := sc.Read(buf)
	if string(buf[:n]) != "ping" {
		t.Fatalf("got %q", buf[:n])
	}

	// active close (クライアント) → FIN_WAIT_1 → FIN_WAIT_2 → TIME_WAIT → CLOSED
	c.Close()
	if _, err := sc.Read(buf); err != io.EOF {
		t.Fatalf("server read after FIN: %v", err)
	}
	if st := sc.State(); st != StateCloseWait {
		t.Fatalf("server state = %v, want CLOSE_WAIT", st)
	}
	sc.Close() // passive close: CLOSE_WAIT → LAST_ACK → CLOSED
	if !sc.WaitClosed(2*time.Second) || !c.WaitClosed(2*time.Second) {
		t.Fatalf("not closed: client=%v server=%v", c.State(), sc.State())
	}
	if connCount(a) != 0 || connCount(b) != 0 {
		t.Fatal("connection table not cleaned up")
	}
}

func TestTCPConnectRefused(t *testing.T) {
	a, _ := newPair(t, 0)
	if _, err := a.DialTCP(ipB, 12345); err != ErrConnReset {
		t.Fatalf("want ErrConnReset, got %v", err)
	}
}

func tcpBulk(t *testing.T, a, b *Stack, size int) {
	l, _ := b.ListenTCP(7)
	defer l.Close()
	go ServeTCPEcho(l)
	data := make([]byte, size)
	rand.New(rand.NewSource(42)).Read(data)
	start := time.Now()
	got, err := TCPEchoClient(a, ipB, 7, data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("data mismatch: sent %d bytes, got %d", len(data), len(got))
	}
	t.Logf("echoed %d bytes in %v", size, time.Since(start))
}

func TestTCPLargeEcho(t *testing.T) {
	a, b := newPair(t, 0)
	tcpBulk(t, a, b, 100*1024) // 受信ウィンドウ (64KB) より大きい
}

func TestTCPLossyLink(t *testing.T) {
	// 両方向で 10% のフレームを落とす。再送が無ければ 100KB は絶対に届かない。
	for seed := int64(1); seed <= 3; seed++ {
		var da, db LinkDevice
		da, db = NewChanWire()
		la, lb := NewLossyLink(da, 0.10, seed), NewLossyLink(db, 0.10, seed+100)
		a, b := NewStack(la, macA, ipA, nil), NewStack(lb, macB, ipB, nil)
		a.TimeWait, b.TimeWait = 100*time.Millisecond, 100*time.Millisecond
		tcpBulk(t, a, b, 100*1024)
		a.Close()
		b.Close()
		la.mu.Lock()
		lb.mu.Lock()
		t.Logf("seed %d: dropped frames A->B %d, B->A %d", seed, la.Dropped, lb.Dropped)
		if la.Dropped+lb.Dropped == 0 {
			t.Fatal("expected some drops")
		}
		lb.mu.Unlock()
		la.mu.Unlock()
	}
}

func TestHTTP(t *testing.T) {
	a, b := newPair(t, 0)
	l, _ := b.ListenTCP(80)
	go ServeHTTP(l)
	resp, err := HTTPGet(a, ipB, 80, "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix([]byte(resp), []byte("HTTP/1.0 200 OK")) || !bytes.Contains([]byte(resp), []byte("path=/index.html")) {
		t.Fatalf("bad response: %q", resp)
	}
}

func TestPcap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pcap")
	pw, err := NewPcapWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	da, db := NewChanWire()
	a, b := NewStack(da, macA, ipA, pw), NewStack(db, macB, ipB, nil)
	a.Ping(ipB, 1, 8, time.Second)
	a.Close()
	b.Close()
	pw.Close()
	raw, _ := os.ReadFile(path)
	// グローバルヘッダ 24 + (ARP req, ARP reply, ICMP req, ICMP reply) × (16 + 60 以上)
	if len(raw) < 24+4*(16+60) || raw[0] != 0xd4 || raw[3] != 0xa1 {
		t.Fatalf("pcap looks wrong: %d bytes", len(raw))
	}
}

func connCount(s *Stack) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tcpConns)
}
