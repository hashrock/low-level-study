package mininet

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeTCP(t *testing.T) {
	seg := TCPSegment{SrcPort: 49152, DstPort: 7, Seq: 1000, Ack: 2000, Flags: tcpPSH | tcpACK, Window: 512, Payload: []byte("hello")}
	ip := IPv4Header{ID: 1, FlagsOff: ipFlagDF, TTL: 64, Protocol: ProtoTCP, Src: ipB, Dst: ipA}
	frame := EthernetHeader{Dst: macA, Src: macB, Type: EtherTypeIPv4}.Marshal(ip.Marshal(seg.Marshal(ipB, ipA)))
	ls := Decode(frame)
	var names []string
	for _, l := range ls {
		names = append(names, l.Name)
	}
	if got := strings.Join(names, ","); got != "Ethernet II,IPv4,TCP,Data,Padding" {
		t.Fatalf("layers = %s", got)
	}
	if ls[2].Start != 34 || ls[2].End != 54 || ls[3].Start != 54 || ls[3].End != 59 || ls[4].End != 60 {
		t.Fatalf("ranges: %+v", ls)
	}
	for _, f := range append(ls[1].Fields, ls[2].Fields...) {
		if strings.Contains(f.Name, "チェックサム") && !strings.Contains(f.Value, "正しい") {
			t.Errorf("%s = %s", f.Name, f.Value)
		}
	}
	if s := Summarize(frame); s != "TCP 49152 → 7 [PSH,ACK] seq=1000 ack=2000 win=512 len=5" {
		t.Fatalf("summary = %q", s)
	}
}

func TestDecodeARP(t *testing.T) {
	p := ARPPacket{Op: arpOpRequest, SHA: macA, SPA: ipA, TPA: ipB}
	frame := EthernetHeader{Dst: BroadcastMAC, Src: macA, Type: EtherTypeARP}.Marshal(p.Marshal())
	ls := Decode(frame)
	if len(ls) != 3 || ls[1].Name != "ARP" || ls[1].End != 42 || ls[2].Name != "Padding" {
		t.Fatalf("%+v", ls)
	}
	if s := Summarize(frame); s != "ARP who-has 10.0.0.2? tell 10.0.0.1" {
		t.Fatalf("summary %q", s)
	}
}

func TestSimWire(t *testing.T) {
	w, da, db := NewSimWire(30*time.Millisecond, 1)
	defer w.Close()
	var sent, delivered []uint64
	w.OnSend = func(f *WireFrame) { sent = append(sent, f.ID) }
	w.OnDeliver = func(f *WireFrame) { delivered = append(delivered, f.ID) }
	start := time.Now()
	da.Send([]byte("one"))
	da.Send([]byte("two"))
	f, _ := db.Recv()
	if string(f) != "one" || time.Since(start) < 25*time.Millisecond {
		t.Fatalf("got %q after %v", f, time.Since(start))
	}
	if f, _ := db.Recv(); string(f) != "two" {
		t.Fatalf("order: %q", f)
	}
	// 一時停止中は届かない。Step で 1 つだけ届く
	w.SetPaused(true)
	db.Send([]byte("held"))
	time.Sleep(60 * time.Millisecond)
	if st := w.State(); st.InFlight != 1 {
		t.Fatalf("inflight = %d", st.InFlight)
	}
	w.Step()
	if f, _ := da.Recv(); string(f) != "held" {
		t.Fatalf("step: %q", f)
	}
	w.SetPaused(false)
	w.DropNext()
	da.Send([]byte("dropped"))
	da.Send([]byte("ok"))
	if f, _ := db.Recv(); string(f) != "ok" {
		t.Fatalf("drop next: %q", f)
	}
	time.Sleep(10 * time.Millisecond)
	w.tapMu.Lock()
	defer w.tapMu.Unlock()
	if len(sent) != 5 || len(delivered) != 4 {
		t.Fatalf("sent %v delivered %v", sent, delivered)
	}
}
