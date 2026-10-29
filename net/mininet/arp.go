package mininet

// arp.go — ARP (RFC 826): IPv4 アドレス → MAC アドレスの解決
//
//   0        2        4    5    6        8              14       18             24       28
//   +--------+--------+----+----+--------+--------------+--------+--------------+--------+
//   | HType  | PType  |HLen|PLen| Op     | SHA (送信元MAC)| SPA    | THA (宛先MAC) | TPA    |
//   +--------+--------+----+----+--------+--------------+--------+--------------+--------+
//   HType=1 (Ethernet), PType=0x0800 (IPv4), HLen=6, PLen=4, Op: 1=request 2=reply
//
// 送信時の流れ:
//   1. キャッシュに宛先 IP があれば即送信
//   2. なければ IP パケットを pending キューに積み、ブロードキャストで ARP request
//   3. reply が来たらキャッシュに登録し、pending を一気に送信
// request の再送は「キューに積む時に、前回 request から 300ms 以上経っていれば再送」という
// 素朴な方式。上位 (TCP の再送や ping) が再び送ろうとすることで自然にリトライされる。

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

const (
	arpOpRequest = 1
	arpOpReply   = 2
	arpPacketLen = 28

	arpRetryInterval = 300 * time.Millisecond
	arpMaxPending    = 64
)

type ARPPacket struct {
	Op  uint16
	SHA MAC
	SPA IP
	THA MAC
	TPA IP
}

func ParseARP(b []byte) (ARPPacket, error) {
	var p ARPPacket
	if len(b) < arpPacketLen {
		return p, errors.New("arp: too short")
	}
	if binary.BigEndian.Uint16(b[0:2]) != 1 || binary.BigEndian.Uint16(b[2:4]) != EtherTypeIPv4 || b[4] != 6 || b[5] != 4 {
		return p, errors.New("arp: unsupported hardware/protocol")
	}
	p.Op = binary.BigEndian.Uint16(b[6:8])
	copy(p.SHA[:], b[8:14])
	copy(p.SPA[:], b[14:18])
	copy(p.THA[:], b[18:24])
	copy(p.TPA[:], b[24:28])
	return p, nil
}

func (p ARPPacket) Marshal() []byte {
	b := make([]byte, arpPacketLen)
	binary.BigEndian.PutUint16(b[0:2], 1)
	binary.BigEndian.PutUint16(b[2:4], EtherTypeIPv4)
	b[4], b[5] = 6, 4
	binary.BigEndian.PutUint16(b[6:8], p.Op)
	copy(b[8:14], p.SHA[:])
	copy(b[14:18], p.SPA[:])
	copy(b[18:24], p.THA[:])
	copy(b[24:28], p.TPA[:])
	return b
}

type arpTable struct {
	mu      sync.Mutex
	cache   map[IP]MAC
	pending map[IP][][]byte // MAC 解決待ちの IP パケット
	lastReq map[IP]time.Time
}

func newARPTable() *arpTable {
	return &arpTable{cache: map[IP]MAC{}, pending: map[IP][][]byte{}, lastReq: map[IP]time.Time{}}
}

// Lookup はキャッシュを引く (テストやデバッグ用)。
func (s *Stack) ARPLookup(ip IP) (MAC, bool) {
	s.arp.mu.Lock()
	defer s.arp.mu.Unlock()
	m, ok := s.arp.cache[ip]
	return m, ok
}

func (s *Stack) handleARP(b []byte) {
	p, err := ParseARP(b)
	if err != nil {
		return
	}
	t := s.arp
	t.mu.Lock()
	// RFC 826 のマージ処理: 送信元が既知なら更新、自分宛てなら新規登録
	_, known := t.cache[p.SPA]
	var flush [][]byte
	if known || p.TPA == s.IP {
		if !known {
			s.logf("arp: learned %v is-at %v", p.SPA, p.SHA)
		}
		t.cache[p.SPA] = p.SHA
		flush = t.pending[p.SPA]
		delete(t.pending, p.SPA)
		delete(t.lastReq, p.SPA)
	}
	t.mu.Unlock()

	for _, pkt := range flush {
		s.sendFrame(p.SHA, EtherTypeIPv4, pkt)
	}
	if p.TPA == s.IP && p.Op == arpOpRequest {
		reply := ARPPacket{Op: arpOpReply, SHA: s.MAC, SPA: s.IP, THA: p.SHA, TPA: p.SPA}
		s.sendFrame(p.SHA, EtherTypeARP, reply.Marshal())
	}
}

// resolveAndSend は IP パケットを Ethernet に載せて送る。MAC が未解決なら ARP を走らせる。
func (s *Stack) resolveAndSend(dst IP, ipPacket []byte) {
	t := s.arp
	t.mu.Lock()
	if mac, ok := t.cache[dst]; ok {
		t.mu.Unlock()
		s.sendFrame(mac, EtherTypeIPv4, ipPacket)
		return
	}
	q := append(t.pending[dst], ipPacket)
	if len(q) > arpMaxPending {
		q = q[1:] // 古いものから捨てる
	}
	t.pending[dst] = q
	needReq := time.Since(t.lastReq[dst]) >= arpRetryInterval
	if needReq {
		t.lastReq[dst] = time.Now()
	}
	t.mu.Unlock()

	if needReq {
		s.logf("arp: who-has %v tell %v", dst, s.IP)
		req := ARPPacket{Op: arpOpRequest, SHA: s.MAC, SPA: s.IP, TPA: dst}
		s.sendFrame(BroadcastMAC, EtherTypeARP, req.Marshal())
	}
}
