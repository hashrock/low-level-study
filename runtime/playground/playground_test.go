package playground

import (
	"strings"
	"testing"

	"minirt/minirt"
)

func run(t *testing.T, cfg Config) (*Session, []minirt.Event, StepResult) {
	t.Helper()
	var evs []minirt.Event
	s, err := NewSession(cfg, func(e minirt.Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatal(err)
	}
	res := s.Run(100000)
	return s, evs, res
}

func consoleLines(evs []minirt.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Type == minirt.EvConsole && e.Stream == "out" {
			out = append(out, e.Text)
		}
	}
	return out
}

// The README documents the CLI output of timers.js; the browser session
// (virtual clock, traced) must print exactly the same.
func TestTimersMatchesCLI(t *testing.T) {
	src := Examples()["timers.js"]
	s, evs, res := run(t, Config{Name: "timers.js", Source: src, TimeoutMs: 30000})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	want := []string{"1 sync", "2 sync end", "3 micro (promise.then)", "4 micro (queueMicrotask)",
		"5 micro (after await)", "6 timeout 0", "7 micro inside timeout (runs before next timer)",
		"8 timeout 0 (second)", "9 timeout 10", "interval tick 1", "interval tick 2", "interval tick 3", "done"}
	if got := consoleLines(evs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("trace console mismatch\n got %q\nwant %q", got, want)
	}
	if got := strings.TrimSpace(s.Stdout()); got != strings.Join(want, "\n") {
		t.Errorf("stdout mismatch: %q", got)
	}
	if res.Snapshot.Now != 60 {
		t.Errorf("virtual clock should end at 60ms (3 x 20ms interval), got %v", res.Snapshot.Now)
	}
}

func TestAsyncFSOnVirtualFS(t *testing.T) {
	src := Examples()["async_fs.js"]
	s, evs, res := run(t, Config{Name: "async_fs.js", Source: src, FSLatencyMs: 2,
		AllowRead: Grant{On: true, Value: "examples/data"}, AllowWrite: Grant{On: true, Value: "examples/data"}})
	if res.Error != "" {
		t.Fatal(res.Error, s.Stderr())
	}
	out := strings.Join(consoleLines(evs), "\n")
	for _, want := range []string{
		`read: "hello from data/input.txt\n"`,
		"parallel reads equal: true elapsed>=20ms: true",
		`wrote: "written at step 3, length=26\n"`,
		"missing file -> NotFound",
		"require: hello, minirt (from data/lib.js)",
		"permissions.query read examples/data: true",
		"permissions.query read /etc/passwd: false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	found := false
	for _, f := range s.Files() {
		if f.Path == Cwd+"/examples/data/output.txt" && strings.HasPrefix(f.Content, "written at step 3") {
			found = true
		}
	}
	if !found {
		t.Error("output.txt not written to the virtual FS")
	}
}

func TestSandboxEscapeDeniedAndTimesOut(t *testing.T) {
	src := Examples()["sandbox_escape.js"]
	_, evs, res := run(t, Config{Name: "sandbox_escape.js", Source: src, TimeoutMs: 300,
		AllowRead: Grant{On: true, Value: "examples/data"}})
	if res.ExitCode != 124 {
		t.Errorf("want timeout exit 124, got %d (%s)", res.ExitCode, res.Error)
	}
	out := strings.Join(consoleLines(evs), "\n")
	if strings.Contains(out, "ESCAPED") {
		t.Fatalf("escape succeeded:\n%s", out)
	}
	if n := strings.Count(out, "blocked "); n != 11 {
		t.Errorf("want 11 blocked lines, got %d:\n%s", n, out)
	}
	// Permission log shows the normalized path of each ../ and symlink escape.
	want := map[string]string{
		"examples/data/../../go.mod":                 "/app/go.mod",
		"examples/data/../../../../../../etc/passwd": "/etc/passwd",
		"examples/data/link_to_etc/passwd":           "/etc/passwd",
		"/app/examples/../main.go":                   "/app/main.go",
	}
	for _, e := range evs {
		if e.Type != minirt.EvPerm {
			continue
		}
		if p, ok := want[e.Target]; ok {
			if e.Allowed || e.Path != p {
				t.Errorf("perm %q: allowed=%v path=%q, want denied %q", e.Target, e.Allowed, e.Path, p)
			}
			delete(want, e.Target)
		}
	}
	if len(want) != 0 {
		t.Errorf("perm events missing for %v", want)
	}
}

func TestFetchFakeNetworkAndRedirect(t *testing.T) {
	src := `
	(async () => {
	  const r = await fetch("https://example.com/");
	  console.log(r.status, (await r.text()).match(/<title>(.*?)<\/title>/)[1], Date.now() - start);
	  try { await fetch("https://example.com/redirect"); console.log("redirect ALLOWED"); }
	  catch (e) { console.log("redirect", e.name); }
	  const u = await (await fetch("https://api.example.com/users/1")).json();
	  console.log(u.name);
	})();
	const start = Date.now();`
	_, evs, res := run(t, Config{Source: src, AllowNet: Grant{On: true, Value: "example.com,api.example.com"}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	got := strings.Join(consoleLines(evs), "|")
	if got != "200 Example Domain 120|redirect PermissionDenied|Ada" {
		t.Errorf("got %q", got)
	}
	// op.start carries the virtual due time = start + latency.
	for _, e := range evs {
		if e.Type == minirt.EvOpStart && e.Target == "GET https://example.com/" && e.Due != 120 {
			t.Errorf("fetch due = %v, want 120", e.Due)
		}
	}
}

func TestQuizTraceShape(t *testing.T) {
	src := Examples()["event_loop_quiz.js"]
	_, evs, res := run(t, Config{Name: "event_loop_quiz.js", Source: src})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	want := []string{"script start", "worker: before first await", "script end", "then #1",
		"worker: after await null", "queueMicrotask", "then #2 (chained)", "timeout 0",
		"then inside timeout 0", "worker: after awaiting a timer", "timeout 5"}
	if got := consoleLines(evs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
	// Every micro.run must be preceded by its micro.register and happen
	// between a task.body.end and the task.end of the same task.
	registered := map[int64]bool{}
	inDrain := false
	for _, e := range evs {
		switch e.Type {
		case minirt.EvMicroRegister:
			registered[e.ID] = true
		case minirt.EvTaskBodyEnd:
			inDrain = true
		case minirt.EvTaskEnd:
			inDrain = false
		case minirt.EvMicroRun:
			if !registered[e.ID] {
				t.Errorf("micro.run %d without register", e.ID)
			}
			if !inDrain {
				t.Errorf("micro.run %d outside a microtask drain", e.ID)
			}
		}
	}
}

func TestStepIsOneTask(t *testing.T) {
	s, err := NewSession(Config{Source: `setTimeout(() => console.log("a"), 10); setTimeout(() => console.log("b"), 20);`}, func(minirt.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Step() // main
	if r.Done || len(r.Snapshot.Timers) != 2 || r.Snapshot.Now != 0 {
		t.Fatalf("after main: %+v", r)
	}
	r = s.Step()
	if r.Done || s.Stdout() != "a\n" || r.Snapshot.Now != 10 || len(r.Snapshot.Timers) != 1 {
		t.Fatalf("after step 2: %+v %q", r, s.Stdout())
	}
	r = s.Step()
	if !r.Done || s.Stdout() != "a\nb\n" || r.Snapshot.Now != 20 {
		t.Fatalf("after step 3: %+v %q", r, s.Stdout())
	}
}

func TestFlags(t *testing.T) {
	c := Config{Name: "x.js", AllowRead: Grant{On: true, Value: "examples/data"}, AllowNet: Grant{On: true}, TimeoutMs: 2000}
	if got := c.Flags(); got != "minirt examples/x.js --allow-read=examples/data --allow-net --timeout=2000ms" {
		t.Errorf("Flags() = %q", got)
	}
}
