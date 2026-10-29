package mininet

// ethernet.go — Ethernet II フレーム
//
//  0                   6                   12      14
//  +-------------------+-------------------+-------+------------------+
//  | 宛先 MAC (6)      | 送信元 MAC (6)    | Type  | ペイロード        |
//  +-------------------+-------------------+-------+------------------+
//  Type: 0x0800 = IPv4, 0x0806 = ARP
//
// 本物の NIC は末尾に FCS (CRC32, 4 bytes) を付けて送るが、OS に渡る時点で
// 外されているのが普通なので、ここでも扱わない。最小フレーム長は 60 bytes
// (FCS 抜き) なので、短いフレームは 0 でパディングする。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type MAC [6]byte

var BroadcastMAC = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

func ParseMAC(s string) (MAC, error) {
	var m MAC
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return m, fmt.Errorf("bad MAC %q", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return m, fmt.Errorf("bad MAC %q", s)
		}
		m[i] = byte(v)
	}
	return m, nil
}

func (m MAC) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}

const (
	EtherTypeIPv4 = 0x0800
	EtherTypeARP  = 0x0806

	ethHeaderLen   = 14
	ethMinFrameLen = 60
)

type EthernetHeader struct {
	Dst  MAC
	Src  MAC
	Type uint16
}

func ParseEthernet(b []byte) (EthernetHeader, []byte, error) {
	var h EthernetHeader
	if len(b) < ethHeaderLen {
		return h, nil, errors.New("ethernet: frame too short")
	}
	copy(h.Dst[:], b[0:6])
	copy(h.Src[:], b[6:12])
	h.Type = binary.BigEndian.Uint16(b[12:14])
	return h, b[ethHeaderLen:], nil
}

func (h EthernetHeader) Marshal(payload []byte) []byte {
	n := max(ethHeaderLen+len(payload), ethMinFrameLen)
	b := make([]byte, n) // パディング部分は 0 のまま
	copy(b[0:6], h.Dst[:])
	copy(b[6:12], h.Src[:])
	binary.BigEndian.PutUint16(b[12:14], h.Type)
	copy(b[ethHeaderLen:], payload)
	return b
}
