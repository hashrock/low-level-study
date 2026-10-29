package sim

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// waitOp は操作 id の結果イベントを待つ。
func waitOp(t *testing.T, s *Session, id int, timeout time.Duration) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range s.Poll(0, 1<<30).Events {
			if e.K == "op" && e.Op == id && e.Status != "start" {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("op %d did not finish", id)
	return Event{}
}

func newSession(t *testing.T, latency time.Duration) *Session {
	s := New()
	s.SetLatency(latency)
	s.A.TimeWait, s.B.TimeWait = 100*time.Millisecond, 100*time.Millisecond
	t.Cleanup(s.Close)
	return s
}

func labels(s *Session) []string {
	var out []string
	for _, e := range s.Poll(0, 1<<30).Events {
		if e.K == "tx" {
			out = append(out, e.Dir+" "+e.Label)
		}
	}
	return out
}

func TestSessionPingAndHandshake(t *testing.T) {
	s := newSession(t, 5*time.Millisecond)
	if e := waitOp(t, s, s.Run("ping", ""), 5*time.Second); e.Status != "ok" {
		t.Fatalf("ping: %+v", e)
	}
	if e := waitOp(t, s, s.Run("tcp-echo", "hello"), 5*time.Second); e.Status != "ok" || !strings.Contains(e.Msg, `"hello"`) {
		t.Fatalf("tcp-echo: %+v", e)
	}
	ls := labels(s)
	joined := strings.Join(ls, "\n")
	for _, want := range []string{"BA ARP who-has 10.0.0.1?", "AB ARP 10.0.0.1 is-at 02:00:00:00:00:01",
		"BA ICMP echo request seq=0", "AB ICMP echo reply seq=0",
		"BA [SYN] seq=0", "AB [SYN,ACK] seq=0 ack=1", "BA [ACK] ack=1", "BA [PSH,ACK] seq=1 ack=1 len=5"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	// 状態遷移: B は CLOSED→SYN_SENT→ESTABLISHED→FIN_WAIT_1→…→TIME_WAIT→CLOSED
	time.Sleep(300 * time.Millisecond)
	var bStates []string
	for _, e := range s.Poll(0, 1<<30).Events {
		if e.K == "state" && e.Node == "B" {
			bStates = append(bStates, e.To)
		}
	}
	got := strings.Join(bStates, " ")
	if !strings.HasPrefix(got, "SYN_SENT ESTABLISHED FIN_WAIT_1") || !strings.HasSuffix(got, "TIME_WAIT CLOSED") {
		t.Fatalf("B states: %s", got)
	}
	// フレーム詳細と pcap
	d, ok := s.Frame(1)
	if !ok || d.Layers[0].Name != "Ethernet II" || d.Layers[1].Name != "ARP" || len(d.Hex) != 2*d.Len {
		t.Fatalf("frame 1: %+v", d)
	}
	p := s.PCAP(true)
	if len(p) < 24+10*(16+60) || !bytes.Equal(p[:4], []byte{0xd4, 0xc3, 0xb2, 0xa1}) {
		t.Fatalf("pcap %d bytes", len(p))
	}
}

func TestSessionRSTAndHTTP(t *testing.T) {
	s := newSession(t, 2*time.Millisecond)
	if e := waitOp(t, s, s.Run("rst", ""), 5*time.Second); e.Status != "ok" {
		t.Fatalf("rst: %+v", e)
	}
	if !strings.Contains(strings.Join(labels(s), "\n"), "AB [RST,ACK] seq=0 ack=1") {
		t.Fatalf("no RST: %v", labels(s))
	}
	if e := waitOp(t, s, s.Run("http", "/x"), 5*time.Second); e.Status != "ok" || !strings.HasPrefix(e.Msg, "HTTP/1.0 200 OK") {
		t.Fatalf("http: %+v", e)
	}
	if e := waitOp(t, s, s.Run("udp", "hi"), 5*time.Second); e.Status != "ok" {
		t.Fatalf("udp: %+v", e)
	}
}

func TestSessionBulkWithLoss(t *testing.T) {
	s := newSession(t, 2*time.Millisecond)
	s.Run("ping", "") // ARP を先に解決しておく (損失で ARP が長引かないように)
	time.Sleep(200 * time.Millisecond)
	s.SetLoss(0.15)
	e := waitOp(t, s, s.Run("bulk", "32"), 60*time.Second)
	if e.Status != "ok" || !strings.Contains(e.Msg, "integrity OK") {
		t.Fatalf("bulk: %+v", e)
	}
	var retx, drops, dups int
	for _, e := range s.Poll(0, 1<<30).Events {
		if e.K == "tx" && e.Retx {
			retx++
		}
		if e.K == "tx" && e.Drop {
			drops++
		}
		if e.Dup > 0 {
			dups++
		}
	}
	t.Logf("drops=%d retx=%d dupacks=%d", drops, retx, dups)
	if drops == 0 || retx == 0 {
		t.Fatal("expected drops and retransmissions")
	}
	if s.PCAP(false) == nil || len(s.PCAP(false)) >= len(s.PCAP(true)) {
		t.Fatal("pcap without drops should be smaller")
	}
}

func TestSessionPauseStepDrop(t *testing.T) {
	s := newSession(t, 1*time.Millisecond)
	s.SetPaused(true)
	id := s.Run("ping", "")
	time.Sleep(100 * time.Millisecond)
	if st := s.Wire.State(); st.InFlight == 0 {
		t.Fatal("expected held frames while paused")
	}
	if !s.DropHead() { // ARP who-has を捨てる
		t.Fatal("DropHead")
	}
	for i := 0; i < 50 && s.Wire.State().InFlight == 0; i++ {
		time.Sleep(50 * time.Millisecond) // ping の再送で ARP がもう一度飛ぶのを待つ
	}
	if !s.Step() {
		t.Fatal("Step")
	}
	s.SetPaused(false)
	if e := waitOp(t, s, id, 10*time.Second); e.Status != "ok" {
		t.Fatalf("ping: %+v", e)
	}
	var sawDrop bool
	for _, e := range s.Poll(0, 1<<30).Events {
		if e.K == "drop" && e.Reason == "manual" {
			sawDrop = true
		}
	}
	if !sawDrop {
		t.Fatal("no drop event")
	}
	s.DropNext()
	before := s.Poll(0, 1<<30).Next
	s.Run("udp", "x")
	time.Sleep(100 * time.Millisecond)
	for _, e := range s.Poll(before, 1<<30).Events {
		if e.K == "tx" {
			if !e.Drop || e.Reason != "manual" {
				t.Fatalf("first frame after DropNext not dropped: %+v", e)
			}
			return
		}
	}
	t.Fatal("no frame after DropNext")
}

func TestPollOrdering(t *testing.T) {
	s := newSession(t, 0)
	waitOp(t, s, s.Run("tcp-echo", "abc"), 5*time.Second)
	// rx は必ず対応する tx の後に来る
	seen := map[uint64]bool{}
	next := 0
	for {
		r := s.Poll(next, 7)
		if len(r.Events) == 0 {
			break
		}
		for _, e := range r.Events {
			switch e.K {
			case "tx":
				seen[e.ID] = true
			case "rx", "drop":
				if !seen[e.ID] {
					t.Fatalf("event %s for frame %d before its tx", e.K, e.ID)
				}
			}
		}
		next = r.Next
	}
}
