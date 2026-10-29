package sim

// flows.go — ケーブル上のフレームに「シーケンス図のラベル」を付ける。
//
// Wireshark と同じく TCP のシーケンス番号は相対値 (ISN を 0 とする) で表示し、
// 次のものに印を付ける:
//   - 再送: 送信側がすでに送ったシーケンス範囲をもう一度送った (捨てられたフレームの再送も含む)
//   - 重複 ACK: 前と同じ ack・同じ window の純粋な ACK で、相手側に未 ACK のデータがある

import (
	"fmt"
	"strings"

	"mininet/mininet"
)

type flowState struct {
	isn     uint32
	hasISN  bool
	maxEnd  uint32 // これまでに送ったシーケンス空間の終わり
	hasMax  bool
	lastAck uint32
	lastWin uint16
	hasAck  bool
	dups    int
}

func seqLT(a, b uint32) bool { return int32(a-b) < 0 }

func (s *Session) flow(key string) *flowState {
	f := s.flows[key]
	if f == nil {
		f = &flowState{}
		s.flows[key] = f
	}
	return f
}

// onSend は SimWire のタップ。フレームを記録し、tx イベントを出す。
func (s *Session) onSend(f *mininet.WireFrame) {
	e := Event{K: "tx", ID: f.ID, T: s.ms(f.SentAt), Dir: dirName(f.Dir), Len: len(f.Data),
		Drop: f.Dropped, Reason: f.DropReason}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.classify(f.Data, &e)
	s.frames = append(s.frames, frameRec{f: f, label: e.Label, retx: e.Retx, dropped: f.Dropped, reason: f.DropReason})
	e.Seq = len(s.events)
	s.events = append(s.events, e)
	s.mu.Unlock()
}

// classify はフレームを解析して e.Proto / Label / Flags / Pair / Retx / Dup を埋める (s.mu を持って呼ぶ)。
func (s *Session) classify(frame []byte, e *Event) {
	eth, payload, err := mininet.ParseEthernet(frame)
	if err != nil {
		e.Proto, e.Label = "?", "?"
		return
	}
	switch eth.Type {
	case mininet.EtherTypeARP:
		e.Proto = "arp"
		p, err := mininet.ParseARP(payload)
		if err != nil {
			e.Label = "ARP ?"
			return
		}
		if p.Op == 1 {
			e.Label = fmt.Sprintf("ARP who-has %v?", p.TPA)
		} else {
			e.Label = fmt.Sprintf("ARP %v is-at %v", p.SPA, p.SHA)
		}
		return
	case mininet.EtherTypeIPv4:
	default:
		e.Proto, e.Label = "?", fmt.Sprintf("EtherType 0x%04x", eth.Type)
		return
	}
	ip, l4, err := mininet.ParseIPv4(payload)
	if err != nil {
		e.Proto, e.Label = "ip", "IPv4 (不正)"
		return
	}
	switch ip.Protocol {
	case mininet.ProtoICMP:
		e.Proto = "icmp"
		m, err := mininet.ParseICMPEcho(l4)
		if err != nil {
			e.Label = "ICMP ?"
			return
		}
		kind := "echo request"
		if m.Type == 0 {
			kind = "echo reply"
		}
		e.Label = fmt.Sprintf("ICMP %s seq=%d", kind, m.Seq)
	case mininet.ProtoUDP:
		e.Proto = "udp"
		if len(l4) >= 8 {
			sp, dp := uint16(l4[0])<<8|uint16(l4[1]), uint16(l4[2])<<8|uint16(l4[3])
			e.Label = fmt.Sprintf("UDP %d→%d len=%d", sp, dp, len(l4)-8)
		}
	case mininet.ProtoTCP:
		e.Proto = "tcp"
		seg, err := mininet.ParseTCP(ip.Src, ip.Dst, l4)
		if err != nil {
			e.Label = "TCP (不正)"
			return
		}
		s.classifyTCP(ip, seg, e)
	default:
		e.Proto, e.Label = "ip", fmt.Sprintf("IPv4 proto=%d", ip.Protocol)
	}
}

const (
	fFIN = 0x01
	fSYN = 0x02
	fRST = 0x04
	fACK = 0x10
)

func (s *Session) classifyTCP(ip mininet.IPv4Header, seg mininet.TCPSegment, e *Event) {
	key := fmt.Sprintf("%v:%d>%v:%d", ip.Src, seg.SrcPort, ip.Dst, seg.DstPort)
	rkey := fmt.Sprintf("%v:%d>%v:%d", ip.Dst, seg.DstPort, ip.Src, seg.SrcPort)
	fw, rv := s.flow(key), s.flow(rkey)
	e.Pair = pairOf(ip.Src, seg.SrcPort, ip.Dst, seg.DstPort)
	e.Flags = mininet.TCPFlagNames(seg.Flags)

	if seg.Flags&fSYN != 0 && (!fw.hasISN || fw.isn != seg.Seq) {
		*fw = flowState{isn: seg.Seq, hasISN: true} // 新しいコネクション
		if seg.Flags&fACK == 0 {
			*rv = flowState{} // 逆方向の古い情報も捨てる
		}
	}
	occ := uint32(len(seg.Payload))
	if seg.Flags&fSYN != 0 {
		occ++
	}
	if seg.Flags&fFIN != 0 {
		occ++
	}
	if occ > 0 {
		if fw.hasMax && seqLT(seg.Seq, fw.maxEnd) {
			e.Retx = true
		}
		if end := seg.Seq + occ; !fw.hasMax || seqLT(fw.maxEnd, end) {
			fw.maxEnd, fw.hasMax = end, true
		}
	}
	if seg.Flags&fACK != 0 {
		pure := occ == 0 && seg.Flags&fRST == 0
		if pure && fw.hasAck && seg.Ack == fw.lastAck && seg.Window == fw.lastWin && rv.hasMax && seqLT(seg.Ack, rv.maxEnd) {
			fw.dups++
			e.Dup = fw.dups
		} else if seg.Ack != fw.lastAck || !pure {
			fw.dups = 0
		}
		fw.lastAck, fw.lastWin, fw.hasAck = seg.Ack, seg.Window, true
	}

	rel := func(f *flowState, v uint32) uint32 {
		if f.hasISN {
			return v - f.isn
		}
		return v
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]", e.Flags)
	if occ > 0 || seg.Flags&fRST != 0 {
		fmt.Fprintf(&b, " seq=%d", rel(fw, seg.Seq))
	}
	if seg.Flags&fACK != 0 {
		fmt.Fprintf(&b, " ack=%d", rel(rv, seg.Ack))
	}
	if len(seg.Payload) > 0 {
		fmt.Fprintf(&b, " len=%d", len(seg.Payload))
	}
	if seg.Window != 65535 {
		fmt.Fprintf(&b, " win=%d", seg.Window)
	}
	e.Label = b.String()
}
