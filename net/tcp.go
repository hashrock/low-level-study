package main

// tcp.go — TCP ヘッダと、受信セグメントのコネクションへの振り分け
//
//   0               1               2               3
//   0 1 2 3 4 5 6 7 0 1 2 3 4 5 6 7 0 1 2 3 4 5 6 7 0 1 2 3 4 5 6 7
//  +-------------------------------+-------------------------------+
//  |          Source Port          |       Destination Port        |  0
//  +-------------------------------+-------------------------------+
//  |                        Sequence Number                        |  4
//  +---------------------------------------------------------------+
//  |                    Acknowledgment Number                      |  8
//  +-------+-------+---------------+-------------------------------+
//  | DOff  | Rsvd  |C E U A P R S F|            Window             | 12
//  +-------+-------+---------------+-------------------------------+
//  |           Checksum            |         Urgent Pointer        | 16
//  +-------------------------------+-------------------------------+
//  |                    Options (DOff>5 のとき)                     | 20
//
// DOff = ヘッダ長 / 4。このスタックはオプションを送らない (受信時は読み飛ばす)。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpRST = 0x04
	tcpPSH = 0x08
	tcpACK = 0x10

	tcpHeaderLen = 20
)

type TCPSegment struct {
	SrcPort, DstPort uint16
	Seq, Ack         uint32
	Flags            uint8
	Window           uint16
	Payload          []byte
}

func ParseTCP(src, dst IP, b []byte) (TCPSegment, error) {
	var t TCPSegment
	if len(b) < tcpHeaderLen {
		return t, errors.New("tcp: too short")
	}
	if pseudoHeaderChecksum(src, dst, ProtoTCP, b) != 0 {
		return t, errors.New("tcp: bad checksum")
	}
	off := int(b[12]>>4) * 4
	if off < tcpHeaderLen || off > len(b) {
		return t, errors.New("tcp: bad data offset")
	}
	t.SrcPort = binary.BigEndian.Uint16(b[0:2])
	t.DstPort = binary.BigEndian.Uint16(b[2:4])
	t.Seq = binary.BigEndian.Uint32(b[4:8])
	t.Ack = binary.BigEndian.Uint32(b[8:12])
	t.Flags = b[13]
	t.Window = binary.BigEndian.Uint16(b[14:16])
	t.Payload = b[off:]
	return t, nil
}

func (t TCPSegment) Marshal(src, dst IP) []byte {
	b := make([]byte, tcpHeaderLen+len(t.Payload))
	binary.BigEndian.PutUint16(b[0:2], t.SrcPort)
	binary.BigEndian.PutUint16(b[2:4], t.DstPort)
	binary.BigEndian.PutUint32(b[4:8], t.Seq)
	binary.BigEndian.PutUint32(b[8:12], t.Ack)
	b[12] = (tcpHeaderLen / 4) << 4
	b[13] = t.Flags
	binary.BigEndian.PutUint16(b[14:16], t.Window)
	copy(b[tcpHeaderLen:], t.Payload)
	binary.BigEndian.PutUint16(b[16:18], pseudoHeaderChecksum(src, dst, ProtoTCP, b))
	return b
}

func flagString(f uint8) string {
	var sb strings.Builder
	for _, x := range []struct {
		bit uint8
		c   string
	}{{tcpSYN, "S"}, {tcpFIN, "F"}, {tcpRST, "R"}, {tcpPSH, "P"}, {tcpACK, "."}} {
		if f&x.bit != 0 {
			sb.WriteString(x.c)
		}
	}
	return sb.String()
}

func (t TCPSegment) String() string {
	return fmt.Sprintf("%d > %d [%s] seq=%d ack=%d win=%d len=%d",
		t.SrcPort, t.DstPort, flagString(t.Flags), t.Seq, t.Ack, t.Window, len(t.Payload))
}

// シーケンス番号は 32bit で一周するので、大小比較は差を符号付きで見る。
func seqLT(a, b uint32) bool { return int32(a-b) < 0 }
func seqLE(a, b uint32) bool { return int32(a-b) <= 0 }

// tcpKey はコネクションを一意に決める 4 つ組 (自分の IP は 1 つなので省略)。
type tcpKey struct {
	localPort  uint16
	remoteIP   IP
	remotePort uint16
}

func (s *Stack) handleTCP(ip IPv4Header, b []byte) {
	seg, err := ParseTCP(ip.Src, ip.Dst, b)
	if err != nil {
		s.logf("tcp: drop: %v", err)
		return
	}
	key := tcpKey{localPort: seg.DstPort, remoteIP: ip.Src, remotePort: seg.SrcPort}
	s.mu.Lock()
	c := s.tcpConns[key]
	l := s.listeners[seg.DstPort]
	s.mu.Unlock()

	if c != nil {
		c.handleSegment(seg)
		return
	}
	// LISTEN 中のポートへの SYN → 新しいコネクションを SYN_RCVD で作る
	if l != nil && seg.Flags&(tcpSYN|tcpACK|tcpRST) == tcpSYN {
		c = s.newConn(key, StateSynRcvd)
		c.listener = l
		c.irs = seg.Seq
		c.rcvNxt = seg.Seq + 1
		c.sndWnd = seg.Window
		s.mu.Lock()
		s.tcpConns[key] = c
		s.mu.Unlock()
		c.logf("rx %v", seg)
		c.mu.Lock()
		c.logf("state LISTEN -> SYN_RCVD (new connection)")
		c.output() // SYN+ACK を送る
		c.mu.Unlock()
		return
	}
	// 該当なし → RST を返す (RST には RST を返さない)
	if seg.Flags&tcpRST != 0 {
		return
	}
	rst := TCPSegment{SrcPort: seg.DstPort, DstPort: seg.SrcPort, Flags: tcpRST}
	if seg.Flags&tcpACK != 0 {
		rst.Seq = seg.Ack
	} else {
		rst.Flags |= tcpACK
		rst.Ack = seg.Seq + uint32(len(seg.Payload))
		if seg.Flags&(tcpSYN) != 0 {
			rst.Ack++
		}
		if seg.Flags&(tcpFIN) != 0 {
			rst.Ack++
		}
	}
	s.logf("tcp: no socket for port %d, sending RST", seg.DstPort)
	s.SendIP(ip.Src, ProtoTCP, rst.Marshal(s.IP, ip.Src))
}
