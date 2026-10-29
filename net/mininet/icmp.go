package mininet

// icmp.go — ICMP Echo Request / Reply (ping)
//
//   0      1      2             4             6             8
//   +------+------+-------------+-------------+-------------+-------------+
//   | Type | Code | Checksum    | Identifier  | Sequence    | Data ...    |
//   +------+------+-------------+-------------+-------------+-------------+
//   Type: 8 = Echo Request, 0 = Echo Reply. Checksum は ICMP メッセージ全体に対して計算。

import (
	"encoding/binary"
	"errors"
	"time"
)

const (
	icmpEchoReply   = 0
	icmpEchoRequest = 8
)

type ICMPEcho struct {
	Type, Code uint8
	ID, Seq    uint16
	Data       []byte
}

func ParseICMPEcho(b []byte) (ICMPEcho, error) {
	var m ICMPEcho
	if len(b) < 8 {
		return m, errors.New("icmp: too short")
	}
	if internetChecksum(b) != 0 {
		return m, errors.New("icmp: bad checksum")
	}
	m.Type, m.Code = b[0], b[1]
	m.ID = binary.BigEndian.Uint16(b[4:6])
	m.Seq = binary.BigEndian.Uint16(b[6:8])
	m.Data = b[8:]
	return m, nil
}

func (m ICMPEcho) Marshal() []byte {
	b := make([]byte, 8+len(m.Data))
	b[0], b[1] = m.Type, m.Code
	binary.BigEndian.PutUint16(b[4:6], m.ID)
	binary.BigEndian.PutUint16(b[6:8], m.Seq)
	copy(b[8:], m.Data)
	binary.BigEndian.PutUint16(b[2:4], internetChecksum(b))
	return b
}

func (s *Stack) handleICMP(ip IPv4Header, b []byte) {
	m, err := ParseICMPEcho(b)
	if err != nil {
		return
	}
	switch m.Type {
	case icmpEchoRequest:
		reply := ICMPEcho{Type: icmpEchoReply, ID: m.ID, Seq: m.Seq, Data: m.Data}
		s.SendIP(ip.Src, ProtoICMP, reply.Marshal())
	case icmpEchoReply:
		key := uint32(m.ID)<<16 | uint32(m.Seq)
		s.mu.Lock()
		ch := s.pingWaiters[key]
		delete(s.pingWaiters, key)
		s.mu.Unlock()
		if ch != nil {
			ch <- struct{}{}
		}
	}
}

var ErrTimeout = errors.New("timeout")

// Ping は Echo Request を 1 つ送り、Reply までの往復時間を返す。
func (s *Stack) Ping(dst IP, seq uint16, size int, timeout time.Duration) (time.Duration, error) {
	id := s.pingID
	key := uint32(id)<<16 | uint32(seq)
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.pingWaiters[key] = ch
	s.mu.Unlock()

	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}
	start := time.Now()
	req := ICMPEcho{Type: icmpEchoRequest, ID: id, Seq: seq, Data: data}
	s.SendIP(dst, ProtoICMP, req.Marshal())

	// 初回は ARP 解決待ちでパケットがキューに入るだけなので、少し待って再送も試みる
	retry := time.NewTicker(500 * time.Millisecond)
	defer retry.Stop()
	deadline := time.After(timeout)
	for {
		select {
		case <-ch:
			return time.Since(start), nil
		case <-retry.C:
			if _, ok := s.ARPLookup(dst); !ok {
				s.SendIP(dst, ProtoICMP, req.Marshal()) // ARP 再要求のきっかけ
			}
		case <-deadline:
			s.mu.Lock()
			delete(s.pingWaiters, key)
			s.mu.Unlock()
			return 0, ErrTimeout
		}
	}
}
