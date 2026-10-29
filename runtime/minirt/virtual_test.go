package minirt

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVirtualClockJumps(t *testing.T) {
	var out bytes.Buffer
	rt, err := NewRuntime(Options{VirtualClock: true, Stdout: &out, Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// 10 virtual minutes of timers must take no real time.
	err = rt.RunSource("t.js", `
		const t0 = Date.now();
		setTimeout(() => console.log("late", Date.now() - t0), 600000);
		setTimeout(() => console.log("early", Date.now() - t0), 1000);
	`)
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out.String(), "early 1000", "late 600000")
	if time.Since(start) > 2*time.Second {
		t.Error("virtual time should not sleep")
	}
}

func TestVirtualTimeout(t *testing.T) {
	rt, _ := NewRuntime(Options{VirtualClock: true, Timeout: 100 * time.Millisecond, Stdout: &bytes.Buffer{}})
	err := rt.RunSource("t.js", `setInterval(() => {}, 30)`)
	if !errors.Is(err, ErrTimeout) || ExitCode(err) != 124 {
		t.Fatalf("want virtual timeout, got %v", err)
	}
	if now := rt.Snapshot().Now; now != 100 {
		t.Errorf("clock at timeout = %v, want 100", now)
	}
}

func TestVirtualWallWatchdog(t *testing.T) {
	rt, _ := NewRuntime(Options{VirtualClock: true, Timeout: 100 * time.Millisecond})
	err := rt.RunSource("t.js", `for (;;) {}`)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("busy loop must be interrupted, got %v", err)
	}
}

func TestVirtualOpLatencyOrdering(t *testing.T) {
	m := NewMemFS("/w", 5*time.Millisecond)
	_ = m.AddFile("/w/f.txt", "F")
	g, _ := NewPathGrantFS(m, false, []string{"/w"})
	var out bytes.Buffer
	var evs []Event
	rt, _ := NewRuntime(Options{VirtualClock: true, FS: m, Perms: Permissions{Read: g}, Stdout: &out,
		Trace: func(e Event) { evs = append(evs, e) }})
	err := rt.RunSource("/w/main.js", `
		setTimeout(() => console.log("t4"), 4);
		setTimeout(() => console.log("t5"), 5); // same due time as the read: registered first, runs first
		Sandbox.readTextFile("f.txt").then((s) => console.log("read", s, Date.now() - t0));
		setTimeout(() => console.log("t6"), 6);
		const t0 = Date.now();
	`)
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out.String(), "t4", "t5", "read F 5", "t6")
	var kinds []string
	for _, e := range evs {
		if e.Type == EvTaskStart {
			kinds = append(kinds, e.Kind)
		}
	}
	if got := strings.Join(kinds, ","); got != "main,timer,timer,op,timer" {
		t.Errorf("task kinds = %s", got)
	}
}

// The existing ordering test, with tracing on: instrumentation must not
// change behaviour, and the trace must tell the same story.
func TestTracingPreservesOrdering(t *testing.T) {
	var evs []Event
	src := `
		const log = (s) => console.log(s);
		setTimeout(() => { log("timeout1"); Promise.resolve().then(() => log("micro-in-timeout1")); }, 0);
		setTimeout(() => log("timeout2"), 0);
		Promise.resolve().then(() => log("micro1"));
		queueMicrotask(() => log("micro2"));
		(async () => { await null; log("await"); })();
		Promise.reject(new Error("x")).catch(function onErr() { log("caught"); });
		log("sync");
	`
	for _, virtual := range []bool{false, true} {
		evs = nil
		var out bytes.Buffer
		rt, err := NewRuntime(Options{Stdout: &out, VirtualClock: virtual, Trace: func(e Event) { evs = append(evs, e) }})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.RunSource("t.js", src); err != nil {
			t.Fatal(err)
		}
		expectLines(t, out.String(), "sync", "micro1", "micro2", "await", "caught",
			"timeout1", "micro-in-timeout1", "timeout2")
		var tl []string
		for _, e := range evs {
			switch e.Type {
			case EvTaskStart:
				tl = append(tl, "T:"+e.Kind)
			case EvMicroRegister:
				tl = append(tl, "reg:"+e.Kind+":"+e.State)
			case EvMicroRun:
				tl = append(tl, "run")
			case EvTaskBodyEnd:
				tl = append(tl, "|")
			case EvTimerFire:
				tl = append(tl, "fire")
			}
		}
		want := "T:main reg:then:queued reg:queueMicrotask:queued reg:await:unknown reg:catch:queued | run run run run " +
			"T:timer fire reg:then:queued | run T:timer fire |"
		if got := strings.Join(tl, " "); got != want {
			t.Errorf("virtual=%v trace\n got %s\nwant %s", virtual, got, want)
		}
	}
}

func TestTraceUnhandledThenDropped(t *testing.T) {
	// .catch(f) on a fulfilled promise registers a reaction whose handler
	// never runs; it must be dropped at the end of the task, not linger.
	var evs []Event
	rt, _ := NewRuntime(Options{VirtualClock: true, Stdout: &bytes.Buffer{}, Trace: func(e Event) { evs = append(evs, e) }})
	if err := rt.RunSource("t.js", `Promise.resolve(1).catch(() => {});`); err != nil {
		t.Fatal(err)
	}
	var drop, run int
	for _, e := range evs {
		switch e.Type {
		case EvMicroDrop:
			drop++
		case EvMicroRun:
			run++
		}
	}
	if drop != 1 || run != 0 {
		t.Errorf("drop=%d run=%d, want 1/0", drop, run)
	}
}

func TestTracePermEventsShowResolvedPath(t *testing.T) {
	m := NewMemFS("/w", 0)
	_ = m.AddFile("/w/data/a.txt", "a")
	_ = m.AddFile("/secret.txt", "s")
	g, _ := NewPathGrantFS(m, false, []string{"/w/data"})
	var evs []Event
	rt, _ := NewRuntime(Options{VirtualClock: true, FS: m, Perms: Permissions{Read: g}, Stdout: &bytes.Buffer{},
		Trace: func(e Event) { evs = append(evs, e) }})
	_ = rt.RunSource("t.js", `
		try { Sandbox.readTextFileSync("data/../../secret.txt") } catch (e) { console.log(e.message) }
		Sandbox.readTextFileSync("data/./a.txt");
		try { Sandbox.writeTextFileSync("data/x", "") } catch (e) {}
	`)
	var perms []string
	for _, e := range evs {
		if e.Type == EvPerm {
			perms = append(perms, e.Kind+" "+e.Target+" -> "+e.Path+" "+map[bool]string{true: "allow", false: "deny"}[e.Allowed])
		}
		if e.Type == EvConsole && strings.Contains(e.Text, "/secret.txt") && !strings.Contains(e.Text, "../") {
			t.Errorf("the resolved path must not leak to the script: %q", e.Text)
		}
	}
	want := "read data/../../secret.txt -> /secret.txt deny|read data/./a.txt -> /w/data/a.txt allow|write data/x -> /w/data/x deny"
	if got := strings.Join(perms, "|"); got != want {
		t.Errorf("perm trace\n got %s\nwant %s", got, want)
	}
}

func TestFakeNetworkRealClock(t *testing.T) {
	var out bytes.Buffer
	var evs []Event
	rt, _ := NewRuntime(Options{Stdout: &out, Network: NewFakeNetwork(DefaultFakeRoutes()),
		Perms: Permissions{Net: Grant{Items: []string{"example.com"}}},
		Trace: func(e Event) { evs = append(evs, e) }})
	start := time.Now()
	err := rt.RunSource("t.js", `
		fetch("https://example.com/").then((r) => console.log("status", r.status));
		fetch("https://example.com/redirect").catch((e) => console.log("redirect", e.name));
		fetch("https://nowhere.example.com/").catch((e) => console.log("nowhere", e.name));
	`)
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out.String(), "nowhere PermissionDenied", "redirect PermissionDenied", "status 200")
	if d := time.Since(start); d < 120*time.Millisecond {
		t.Errorf("simulated latency not applied in real mode: %v", d)
	}
	var hop bool
	for _, e := range evs {
		if e.Type == EvPerm && e.Target == "169.254.169.254" && !e.Allowed {
			hop = true // redirect hop re-checked (on the worker goroutine)
		}
	}
	if !hop {
		t.Error("redirect hop permission check not traced")
	}
}
