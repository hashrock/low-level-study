package mininet

// ipv4.go — IPv4 ヘッダとインターネットチェックサム
//
//   0               1               2               3
//   0 1 2 3 4 5 6 7 0 1 2 3 4 5 6 7 0 1 2 3 4 5 6 7 0 1 2 3 4 5 6 7
//  +-------+-------+---------------+-------------------------------+
//  |Version|  IHL  |      TOS      |          Total Length         |  0
//  +-------+-------+---------------+-----+-------------------------+
//  |        Identification         |Flags|     Fragment Offset     |  4
//  +---------------+---------------+-----+-------------------------+
//  |      TTL      |   Protocol    |        Header Checksum        |  8
//  +---------------+---------------+-------------------------------+
//  |                       Source Address                          | 12
//  +---------------------------------------------------------------+
//  |                    Destination Address                        | 16
//  +---------------------------------------------------------------+
//  |                    Options (IHL>5 のとき)                      | 20
//
// 制限: フラグメントの再組み立てはしない (MF フラグか offset != 0 のパケットは捨てる)。
// 送信側は DF を立て、MTU (1500) を超えるパケットは作らない (TCP は MSS で調整)。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type IP [4]byte

func ParseIP(s string) (IP, error) {
	var ip IP
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return ip, fmt.Errorf("bad IPv4 %q", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 10, 8)
		if err != nil {
			return ip, fmt.Errorf("bad IPv4 %q", s)
		}
		ip[i] = byte(v)
	}
	return ip, nil
}

func (ip IP) String() string { return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3]) }

const (
	ProtoICMP = 1
	ProtoTCP  = 6
	ProtoUDP  = 17

	ipv4HeaderLen = 20
	ipFlagDF      = 0x4000
	ipFlagMF      = 0x2000
)

type IPv4Header struct {
	TOS      uint8
	TotalLen uint16
	ID       uint16
	FlagsOff uint16 // 上位 3bit がフラグ, 下位 13bit がフラグメントオフセット
	TTL      uint8
	Protocol uint8
	Checksum uint16
	Src, Dst IP
}

func ParseIPv4(b []byte) (IPv4Header, []byte, error) {
	var h IPv4Header
	if len(b) < ipv4HeaderLen {
		return h, nil, errors.New("ipv4: too short")
	}
	if b[0]>>4 != 4 {
		return h, nil, errors.New("ipv4: not version 4")
	}
	ihl := int(b[0]&0x0f) * 4
	h.TotalLen = binary.BigEndian.Uint16(b[2:4])
	if ihl < ipv4HeaderLen || len(b) < ihl || int(h.TotalLen) > len(b) || int(h.TotalLen) < ihl {
		return h, nil, errors.New("ipv4: bad length")
	}
	if internetChecksum(b[:ihl]) != 0 { // チェックサム欄込みで計算すると 0 になるのが正しい
		return h, nil, errors.New("ipv4: bad checksum")
	}
	h.TOS = b[1]
	h.ID = binary.BigEndian.Uint16(b[4:6])
	h.FlagsOff = binary.BigEndian.Uint16(b[6:8])
	h.TTL = b[8]
	h.Protocol = b[9]
	h.Checksum = binary.BigEndian.Uint16(b[10:12])
	copy(h.Src[:], b[12:16])
	copy(h.Dst[:], b[16:20])
	// TotalLen で切ることで Ethernet のパディングを取り除く
	return h, b[ihl:h.TotalLen], nil
}

func (h IPv4Header) Marshal(payload []byte) []byte {
	b := make([]byte, ipv4HeaderLen+len(payload))
	b[0] = 4<<4 | 5 // Version=4, IHL=5 (20 bytes, オプションなし)
	b[1] = h.TOS
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	binary.BigEndian.PutUint16(b[4:6], h.ID)
	binary.BigEndian.PutUint16(b[6:8], h.FlagsOff)
	b[8] = h.TTL
	b[9] = h.Protocol
	copy(b[12:16], h.Src[:])
	copy(b[16:20], h.Dst[:])
	binary.BigEndian.PutUint16(b[10:12], internetChecksum(b[:ipv4HeaderLen]))
	copy(b[ipv4HeaderLen:], payload)
	return b
}

// internetChecksum は RFC 1071 の 1 の補数和チェックサム。
// 16bit ずつ足し、桁あふれを下位に足し戻し、最後にビット反転する。
func internetChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8 // 奇数長なら末尾に 0 を補ったとみなす
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// pseudoHeaderChecksum は TCP/UDP 用。IP アドレス等の「疑似ヘッダ」を前に付けて計算する。
//
//	0      4      8     9      10        12
//	| src  | dst  | 0   |proto | length  | + TCP/UDP セグメント本体
func pseudoHeaderChecksum(src, dst IP, proto uint8, seg []byte) uint16 {
	b := make([]byte, 12+len(seg))
	copy(b[0:4], src[:])
	copy(b[4:8], dst[:])
	b[9] = proto
	binary.BigEndian.PutUint16(b[10:12], uint16(len(seg)))
	copy(b[12:], seg)
	return internetChecksum(b)
}
