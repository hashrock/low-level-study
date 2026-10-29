package mininet

// decode.go — フレームを層ごとに分解する (Wireshark の中央ペインのようなもの)
//
// 各フィールドがフレームの何バイト目から何バイト目かを持つので、16 進ダンプの
// 対応する範囲を色分けしたりハイライトしたりできる。

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Field は 1 つのヘッダフィールド。[Start, End) はフレーム先頭からのバイト位置。
// 4bit や 3bit のフィールドはそれを含むバイトの範囲になる。
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Layer は 1 つのプロトコル層 (Ethernet, IPv4, TCP, ペイロード, パディング)。
type Layer struct {
	Name    string  `json:"name"`
	Summary string  `json:"summary"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	Fields  []Field `json:"fields"`
}

func be16(b []byte, i int) uint16 { return binary.BigEndian.Uint16(b[i:]) }
func be32(b []byte, i int) uint32 { return binary.BigEndian.Uint32(b[i:]) }

func checksumNote(ok bool) string {
	if ok {
		return " (正しい)"
	}
	return " (不正)"
}

// Decode はフレームを層ごとに分解する。解釈できない部分は "Data" 層として残す。
func Decode(frame []byte) []Layer {
	var layers []Layer
	if len(frame) < ethHeaderLen {
		return []Layer{{Name: "Data", Start: 0, End: len(frame), Summary: fmt.Sprintf("%d bytes (短すぎる)", len(frame))}}
	}
	eth, _, _ := ParseEthernet(frame)
	typeName := map[uint16]string{EtherTypeIPv4: "IPv4", EtherTypeARP: "ARP"}[eth.Type]
	dst := eth.Dst.String()
	if eth.Dst == BroadcastMAC {
		dst += " (broadcast)"
	}
	layers = append(layers, Layer{Name: "Ethernet II", Start: 0, End: ethHeaderLen,
		Summary: fmt.Sprintf("%v → %v", eth.Src, dst),
		Fields: []Field{
			{"宛先 MAC", dst, 0, 6},
			{"送信元 MAC", eth.Src.String(), 6, 12},
			{"EtherType", fmt.Sprintf("0x%04x %s", eth.Type, typeName), 12, 14},
		}})
	end := len(frame) // 有効なデータの終わり (これ以降は Ethernet のパディング)
	off := ethHeaderLen
	switch eth.Type {
	case EtherTypeARP:
		layers, end = decodeARP(layers, frame, off)
	case EtherTypeIPv4:
		layers, end = decodeIPv4(layers, frame, off)
	}
	last := layers[len(layers)-1].End
	if last < end {
		layers = append(layers, Layer{Name: "Data", Start: last, End: end, Summary: dataSummary(frame[last:end])})
	}
	if end < len(frame) {
		layers = append(layers, Layer{Name: "Padding", Start: end, End: len(frame),
			Summary: fmt.Sprintf("%d bytes (最小フレーム長 60 bytes に揃える 0 埋め)", len(frame)-end)})
	}
	return layers
}

func dataSummary(b []byte) string {
	s := fmt.Sprintf("%d bytes", len(b))
	printable := 0
	for _, c := range b {
		if c >= 0x20 && c < 0x7f || c == '\r' || c == '\n' {
			printable++
		}
	}
	if len(b) > 0 && printable == len(b) {
		t := string(b)
		if len(t) > 48 {
			t = t[:48] + "…"
		}
		s += fmt.Sprintf(" %q", t)
	}
	return s
}

func decodeARP(layers []Layer, f []byte, o int) ([]Layer, int) {
	if len(f) < o+arpPacketLen {
		return layers, len(f)
	}
	p, err := ParseARP(f[o:])
	if err != nil {
		return layers, len(f)
	}
	op := map[uint16]string{arpOpRequest: "request (1)", arpOpReply: "reply (2)"}[p.Op]
	sum := fmt.Sprintf("who-has %v? tell %v", p.TPA, p.SPA)
	if p.Op == arpOpReply {
		sum = fmt.Sprintf("%v is-at %v", p.SPA, p.SHA)
	}
	layers = append(layers, Layer{Name: "ARP", Start: o, End: o + arpPacketLen, Summary: sum, Fields: []Field{
		{"ハードウェア種別", fmt.Sprintf("%d (Ethernet)", be16(f, o)), o, o + 2},
		{"プロトコル種別", fmt.Sprintf("0x%04x (IPv4)", be16(f, o+2)), o + 2, o + 4},
		{"HLen", fmt.Sprint(f[o+4]), o + 4, o + 5},
		{"PLen", fmt.Sprint(f[o+5]), o + 5, o + 6},
		{"Op", op, o + 6, o + 8},
		{"送信元 MAC (SHA)", p.SHA.String(), o + 8, o + 14},
		{"送信元 IP (SPA)", p.SPA.String(), o + 14, o + 18},
		{"宛先 MAC (THA)", p.THA.String(), o + 18, o + 24},
		{"宛先 IP (TPA)", p.TPA.String(), o + 24, o + 28},
	}})
	return layers, o + arpPacketLen
}

func decodeIPv4(layers []Layer, f []byte, o int) ([]Layer, int) {
	b := f[o:]
	if len(b) < ipv4HeaderLen || b[0]>>4 != 4 {
		return layers, len(f)
	}
	ihl := int(b[0]&0x0f) * 4
	total := int(be16(b, 2))
	if ihl < ipv4HeaderLen || len(b) < ihl || total < ihl || total > len(b) {
		return layers, len(f)
	}
	var src, dst IP
	copy(src[:], b[12:16])
	copy(dst[:], b[16:20])
	proto := b[9]
	protoName := map[uint8]string{ProtoICMP: "ICMP", ProtoTCP: "TCP", ProtoUDP: "UDP"}[proto]
	fo := be16(b, 6)
	var flags []string
	if fo&ipFlagDF != 0 {
		flags = append(flags, "DF")
	}
	if fo&ipFlagMF != 0 {
		flags = append(flags, "MF")
	}
	layers = append(layers, Layer{Name: "IPv4", Start: o, End: o + ihl,
		Summary: fmt.Sprintf("%v → %v %s", src, dst, protoName),
		Fields: []Field{
			{"バージョン / IHL", fmt.Sprintf("4 / %d (%d bytes)", ihl/4, ihl), o, o + 1},
			{"TOS", fmt.Sprintf("0x%02x", b[1]), o + 1, o + 2},
			{"全長", fmt.Sprint(total), o + 2, o + 4},
			{"ID", fmt.Sprintf("0x%04x (%d)", be16(b, 4), be16(b, 4)), o + 4, o + 6},
			{"フラグ / フラグメントオフセット", fmt.Sprintf("[%s] / %d", strings.Join(flags, ","), fo&0x1fff), o + 6, o + 8},
			{"TTL", fmt.Sprint(b[8]), o + 8, o + 9},
			{"プロトコル", fmt.Sprintf("%d (%s)", proto, protoName), o + 9, o + 10},
			{"ヘッダチェックサム", fmt.Sprintf("0x%04x", be16(b, 10)) + checksumNote(internetChecksum(b[:ihl]) == 0), o + 10, o + 12},
			{"送信元 IP", src.String(), o + 12, o + 16},
			{"宛先 IP", dst.String(), o + 16, o + 20},
		}})
	if ihl > ipv4HeaderLen {
		layers[len(layers)-1].Fields = append(layers[len(layers)-1].Fields, Field{"オプション", fmt.Sprintf("%d bytes", ihl-ipv4HeaderLen), o + 20, o + ihl})
	}
	end := o + total
	l4 := o + ihl
	seg := f[l4:end]
	switch proto {
	case ProtoICMP:
		layers = decodeICMP(layers, seg, l4)
	case ProtoUDP:
		layers = decodeUDP(layers, seg, l4, src, dst)
	case ProtoTCP:
		layers = decodeTCP(layers, seg, l4, src, dst)
	}
	return layers, end
}

func decodeICMP(layers []Layer, b []byte, o int) []Layer {
	if len(b) < 8 {
		return layers
	}
	typ := map[uint8]string{icmpEchoRequest: "Echo Request (8)", icmpEchoReply: "Echo Reply (0)"}[b[0]]
	if typ == "" {
		typ = fmt.Sprint(b[0])
	}
	return append(layers, Layer{Name: "ICMP", Start: o, End: o + 8,
		Summary: fmt.Sprintf("%s id=0x%04x seq=%d", typ, be16(b, 4), be16(b, 6)),
		Fields: []Field{
			{"Type", typ, o, o + 1},
			{"Code", fmt.Sprint(b[1]), o + 1, o + 2},
			{"チェックサム", fmt.Sprintf("0x%04x", be16(b, 2)) + checksumNote(internetChecksum(b) == 0), o + 2, o + 4},
			{"Identifier", fmt.Sprintf("0x%04x", be16(b, 4)), o + 4, o + 6},
			{"Sequence", fmt.Sprint(be16(b, 6)), o + 6, o + 8},
		}})
}

func decodeUDP(layers []Layer, b []byte, o int, src, dst IP) []Layer {
	if len(b) < 8 {
		return layers
	}
	ck := be16(b, 6)
	note := " (なし)"
	if ck != 0 {
		note = checksumNote(pseudoHeaderChecksum(src, dst, ProtoUDP, b) == 0)
	}
	return append(layers, Layer{Name: "UDP", Start: o, End: o + 8,
		Summary: fmt.Sprintf("%d → %d len=%d", be16(b, 0), be16(b, 2), int(be16(b, 4))-8),
		Fields: []Field{
			{"送信元ポート", fmt.Sprint(be16(b, 0)), o, o + 2},
			{"宛先ポート", fmt.Sprint(be16(b, 2)), o + 2, o + 4},
			{"長さ", fmt.Sprint(be16(b, 4)), o + 4, o + 6},
			{"チェックサム", fmt.Sprintf("0x%04x", ck) + note, o + 6, o + 8},
		}})
}

// TCPFlagNames は "SYN,ACK" のようなフラグ名の列を返す。
func TCPFlagNames(f uint8) string {
	var names []string
	for _, x := range []struct {
		bit  uint8
		name string
	}{{tcpSYN, "SYN"}, {tcpFIN, "FIN"}, {tcpRST, "RST"}, {tcpPSH, "PSH"}, {tcpACK, "ACK"}, {0x20, "URG"}, {0x40, "ECE"}, {0x80, "CWR"}} {
		if f&x.bit != 0 {
			names = append(names, x.name)
		}
	}
	return strings.Join(names, ",")
}

func decodeTCP(layers []Layer, b []byte, o int, src, dst IP) []Layer {
	if len(b) < tcpHeaderLen {
		return layers
	}
	doff := int(b[12]>>4) * 4
	if doff < tcpHeaderLen || doff > len(b) {
		doff = tcpHeaderLen
	}
	fl := TCPFlagNames(b[13])
	fields := []Field{
		{"送信元ポート", fmt.Sprint(be16(b, 0)), o, o + 2},
		{"宛先ポート", fmt.Sprint(be16(b, 2)), o + 2, o + 4},
		{"シーケンス番号", fmt.Sprint(be32(b, 4)), o + 4, o + 8},
		{"確認応答番号", fmt.Sprint(be32(b, 8)), o + 8, o + 12},
		{"データオフセット", fmt.Sprintf("%d (%d bytes)", doff/4, doff), o + 12, o + 13},
		{"フラグ", fmt.Sprintf("0x%02x [%s]", b[13], fl), o + 13, o + 14},
		{"ウィンドウ", fmt.Sprint(be16(b, 14)), o + 14, o + 16},
		{"チェックサム", fmt.Sprintf("0x%04x", be16(b, 16)) + checksumNote(pseudoHeaderChecksum(src, dst, ProtoTCP, b) == 0), o + 16, o + 18},
		{"緊急ポインタ", fmt.Sprint(be16(b, 18)), o + 18, o + 20},
	}
	if doff > tcpHeaderLen {
		fields = append(fields, Field{"オプション", fmt.Sprintf("%d bytes", doff-tcpHeaderLen), o + 20, o + doff})
	}
	return append(layers, Layer{Name: "TCP", Start: o, End: o + doff,
		Summary: fmt.Sprintf("%d → %d [%s] seq=%d ack=%d win=%d len=%d", be16(b, 0), be16(b, 2), fl, be32(b, 4), be32(b, 8), be16(b, 14), len(b)-doff),
		Fields:  fields})
}

// Summarize はフレームを 1 行で要約する (最も内側の層の要約)。
func Summarize(frame []byte) string {
	ls := Decode(frame)
	for i := len(ls) - 1; i >= 0; i-- {
		switch ls[i].Name {
		case "Data", "Padding":
			continue
		}
		return ls[i].Name + " " + ls[i].Summary
	}
	return ""
}
