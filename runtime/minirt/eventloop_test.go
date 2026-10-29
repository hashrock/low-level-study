package minirt

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func runJS(t *testing.T, src string, opts Options) (stdout string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	opts.Stdout, opts.Stderr = &out, &errb
	rt, e := NewRuntime(opts)
	if e != nil {
		t.Fatal(e)
	}
	err = rt.RunSource("test.js", src)
	return out.String(), err
}

func lines(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") }

func expectLines(t *testing.T, got string, want ...string) {
	t.Helper()
	g := lines(got)
	if strings.Join(g, "|") != strings.Join(want, "|") {
		t.Errorf("output mismatch\n got: %q\nwant: %q", g, want)
	}
}

func TestEventLoopOrdering(t *testing.T) {
	out, err := runJS(t, `
		const log = (s) => console.log(s);
		setTimeout(() => { log("timeout1"); Promise.resolve().then(() => log("micro-in-timeout1")); }, 0);
		setTimeout(() => log("timeout2"), 0);
		setTimeout(() => log("timeout-late"), 15);
		Promise.resolve().then(() => log("micro1"));
		queueMicrotask(() => log("micro2"));
		(async () => { await null; log("await"); })();
		log("sync");
	`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out, "sync", "micro1", "micro2", "await",
		"timeout1", "micro-in-timeout1", "timeout2", "timeout-late")
}

func TestTimerDelaysAndClear(t *testing.T) {
	out, err := runJS(t, `
		setTimeout(() => console.log("30"), 30);
		setTimeout(() => console.log("10"), 10);
		const x = setTimeout(() => console.log("cleared"), 5);
		clearTimeout(x);
		setTimeout((a, b) => console.log("args", a, b), 20, "A", "B");
		let n = 0;
		const iv = setInterval(() => { if (++n === 3) { clearInterval(iv); console.log("interval x3"); } }, 1);
	`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out, "interval x3", "10", "args A B", "30")
}

func TestAsyncOpResolvesOnLoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, _ := NewPathGrant(false, []string{dir})
	out, err := runJS(t, fmt.Sprintf(`
		Sandbox.readTextFile(%q).then((s) => console.log("read", s));
		Promise.resolve().then(() => console.log("micro"));
		console.log("sync");
	`, filepath.Join(dir, "f.txt")), Options{Perms: Permissions{Read: g}})
	if err != nil {
		t.Fatal(err)
	}
	// The read is a real goroutine round-trip, so it always lands after
	// the current macrotask's microtasks.
	expectLines(t, out, "sync", "micro", "read hi")
}

func TestTimeoutStopsInfiniteLoop(t *testing.T) {
	start := time.Now()
	_, err := runJS(t, `while (true) {}`, Options{Timeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took too long: %v", d)
	}
}

func TestTimeoutStopsIdleInterval(t *testing.T) {
	_, err := runJS(t, `setInterval(() => {}, 10)`, Options{Timeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
}

func TestStackLimitUncatchable(t *testing.T) {
	out, err := runJS(t, `
		function f() { return f(); }
		try { f(); } catch (e) { console.log("caught"); }
	`, Options{MaxCallStack: 200})
	var so *goja.StackOverflowError
	if !errors.As(err, &so) {
		t.Fatalf("want StackOverflowError, got %v", err)
	}
	if strings.Contains(out, "caught") {
		t.Error("stack overflow must not be catchable by the script")
	}
}

func TestUncaughtErrors(t *testing.T) {
	if _, err := runJS(t, `setTimeout(() => { throw new Error("boom") }, 0)`, Options{}); err == nil ||
		!strings.Contains(FormatError(err), "boom") {
		t.Errorf("uncaught timer error not reported: %v", err)
	}
	if _, err := runJS(t, `Promise.reject(new Error("rej"))`, Options{}); err == nil ||
		!strings.Contains(err.Error(), "Uncaught (in promise)") {
		t.Errorf("unhandled rejection not reported: %v", err)
	}
	if _, err := runJS(t, `Promise.reject(1).catch(() => {})`, Options{}); err != nil {
		t.Errorf("handled rejection reported as error: %v", err)
	}
}

func TestSandboxDeniesByDefault(t *testing.T) {
	out, err := runJS(t, `
		const tryIt = async (label, f) => {
			try { await f(); console.log(label, "ALLOWED"); }
			catch (e) { console.log(label, e.name, e instanceof Sandbox.errors.PermissionDenied); }
		};
		(async () => {
			await tryIt("read", () => Sandbox.readTextFile("/etc/passwd"));
			await tryIt("readSync", () => Sandbox.readTextFileSync("/etc/passwd"));
			await tryIt("write", () => Sandbox.writeTextFile("/tmp/minirt-should-not-exist", "x"));
			await tryIt("env", () => Sandbox.env.get("HOME"));
			await tryIt("fetch", () => fetch("http://127.0.0.1:1/"));
			await tryIt("require", () => require("/etc/passwd"));
			console.log(typeof process, typeof Deno);
		})();
	`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out,
		"read PermissionDenied true", "readSync PermissionDenied true",
		"write PermissionDenied true", "env PermissionDenied true",
		"fetch PermissionDenied true", "require PermissionDenied true",
		"undefined undefined")
}

func TestFetchAllowedAndRedirectRechecked(t *testing.T) {
	// Two local servers. Only `allowed` is granted; it redirects to `other`.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secret")
	}))
	defer other.Close()
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"msg":"ok"}`)
	}))
	defer allowed.Close()
	host := strings.TrimPrefix(allowed.URL, "http://")

	out, err := runJS(t, fmt.Sprintf(`
		(async () => {
			const r = await fetch(%q);
			console.log(r.status, r.ok, (await r.json()).msg, r.headers["content-type"]);
			try { await fetch(%q); console.log("redirect ALLOWED"); }
			catch (e) { console.log("redirect", e.name); }
			try { await fetch(%q); console.log("direct ALLOWED"); }
			catch (e) { console.log("direct", e.name); }
		})();
	`, allowed.URL+"/", allowed.URL+"/redirect", other.URL),
		Options{Perms: Permissions{Net: Grant{Items: []string{host}}}})
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out, "200 true ok application/json", "redirect PermissionDenied", "direct PermissionDenied")
}
