// Package minirt is a small sandboxed JavaScript runtime built around goja.
//
// Engine responsibilities (goja): parsing, bytecode, GC, Promise/async-await
// semantics, the microtask (job) queue, built-ins like JSON/Math/Array.
//
// Runtime responsibilities (this package): which globals exist at all,
// the event loop and timers, async host ops, permissions, resource limits,
// error reporting — and, optionally, a trace of what the event loop does.
//
// The outside world is reached only through three interfaces, so the same
// runtime runs against the real OS (CLI) or a simulation (browser build):
// FS (files), Network (fetch) and Options.LookupEnv (environment).
package minirt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dop251/goja"
)

type Options struct {
	Perms        Permissions
	Timeout      time.Duration // 0 = no limit (wall clock; virtual time in virtual mode)
	MaxCallStack int           // max JS call depth
	Args         []string      // exposed as Sandbox.args
	Stdout       io.Writer
	Stderr       io.Writer

	FS        FS                               // nil = OSFS
	Network   Network                          // nil = HTTPNetwork
	LookupEnv func(name string) (string, bool) // nil = os.LookupEnv

	// VirtualClock makes time deterministic: Date.now() and timers use a
	// virtual clock that jumps to the next deadline, op work runs inline
	// and is delivered after its simulated latency. Use Step to single-step.
	VirtualClock bool

	// Trace, if set, receives the event-loop trace (see trace.go). It may be
	// called from worker goroutines (redirect permission checks in real
	// mode), but never concurrently.
	Trace func(Event)
}

type Runtime struct {
	vm    *goja.Runtime
	loop  *Loop
	perms *Permissions
	opts  Options
	fs    FS
	net   Network
	trace *tracer
	micro *microTracker

	errCtors   map[string]goja.Value // Sandbox.errors.*
	rejections map[*goja.Promise]struct{}
	modules    map[string]*goja.Object // require() cache, keyed by canonical path
	jsonMethod goja.Value              // shared Response.prototype.json-like function
	mainDir    string
	stepWall   *time.Timer
}

const bootstrapName = "<minirt>"

// bootstrapJS runs once before user code. Pure JS is fine here because it
// only uses engine built-ins; all capabilities come from Go.
const bootstrapJS = `
(function (g) {
  class SandboxError extends Error {
    constructor(msg) { super(msg); this.name = new.target.name; }
  }
  class PermissionDenied extends SandboxError {}
  class NotFound extends SandboxError {}
  class TimedOut extends SandboxError {}
  g.Sandbox.errors = { PermissionDenied, NotFound, TimedOut };
  g.queueMicrotask = (fn) => { Promise.resolve().then(fn); };
  Object.freeze(g.Sandbox.errors);
})(globalThis);
`

// mainEndName is called at the very end of the main script in traced mode
// (appended to the source) to mark where its own code ends and the microtask
// drain begins. It removes itself on first call.
const mainEndName = "__minirtMainEnd"

func NewRuntime(opts Options) (*Runtime, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.MaxCallStack <= 0 {
		opts.MaxCallStack = 10000
	}
	if opts.FS == nil {
		opts.FS = OSFS{}
	}
	if opts.Network == nil {
		opts.Network = defaultNetwork()
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}
	if opts.Perms.FS == nil {
		opts.Perms.FS = opts.FS
	}
	vm := goja.New()
	// Resource limit #1: call depth. goja raises an uncatchable
	// *goja.StackOverflowError when exceeded, so a script can't swallow it.
	vm.SetMaxCallStackSize(opts.MaxCallStack)
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())

	r := &Runtime{
		vm:         vm,
		loop:       NewLoop(vm),
		opts:       opts,
		fs:         opts.FS,
		net:        opts.Network,
		errCtors:   map[string]goja.Value{},
		rejections: map[*goja.Promise]struct{}{},
		modules:    map[string]*goja.Object{},
	}
	r.perms = &r.opts.Perms
	if opts.VirtualClock {
		r.loop.UseVirtualClock(opts.Timeout)
		vm.SetTimeSource(r.loop.Now)
		if m, ok := opts.FS.(interface{ SetClock(func() time.Time) }); ok {
			m.SetClock(r.loop.Now)
		}
	}
	if opts.Trace != nil {
		r.trace = &tracer{emit: opts.Trace, start: r.loop.start, now: r.loop.Now}
		r.loop.trace = r.trace
	}

	// Unhandled rejections: a rejected promise with no handler at the end of
	// a macrotask is a fatal error (same policy as Deno / modern Node).
	vm.SetPromiseRejectionTracker(func(p *goja.Promise, op goja.PromiseRejectionOperation) {
		if op == goja.PromiseRejectionReject {
			r.rejections[p] = struct{}{}
		} else {
			delete(r.rejections, p)
		}
	})
	r.loop.afterTask = r.afterTask

	sandbox := vm.NewObject()
	if err := vm.Set("Sandbox", sandbox); err != nil {
		return nil, err
	}
	prog := goja.MustCompile(bootstrapName, bootstrapJS, false)
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if r.trace != nil {
		if err := r.installTracing(); err != nil {
			return nil, fmt.Errorf("trace bootstrap: %w", err)
		}
	}
	errs := sandbox.Get("errors").ToObject(vm)
	for _, n := range []string{"PermissionDenied", "NotFound", "TimedOut"} {
		r.errCtors[n] = errs.Get(n)
	}

	r.installConsole()
	r.installTimers()
	r.installFS(sandbox)
	r.installNet()
	r.installEnv(sandbox)
	r.installRequire()

	argv := make([]interface{}, len(opts.Args))
	for i, a := range opts.Args {
		argv[i] = a
	}
	_ = sandbox.Set("args", vm.NewArray(argv...))
	perms := vm.NewObject()
	_ = perms.Set("query", func(kind, target string) bool { return r.perms.Query(kind, target) })
	_ = sandbox.Set("permissions", perms)
	return r, nil
}

func (r *Runtime) installTracing() error {
	fnv, err := r.vm.RunProgram(goja.MustCompile(bootstrapName, traceBootstrapJS, false))
	if err != nil {
		return err
	}
	fn, _ := goja.AssertFunction(fnv)
	hooks := r.vm.NewObject()
	r.installTraceHooks(hooks)
	callTask, err := fn(goja.Undefined(), r.vm.GlobalObject(), hooks)
	if err != nil {
		return err
	}
	r.loop.callTask, _ = goja.AssertFunction(callTask)
	return nil
}

func (r *Runtime) afterTask() error {
	if r.micro != nil {
		r.micro.endOfTask()
	}
	return r.checkRejections()
}

func (r *Runtime) checkRejections() error {
	for p := range r.rejections {
		delete(r.rejections, p)
		return fmt.Errorf("Uncaught (in promise) %s", describeValue(p.Result()))
	}
	return nil
}

// newError builds an instance of one of our JS error classes.
func (r *Runtime) newError(class, msg string) goja.Value {
	if ctor, ok := r.errCtors[class]; ok {
		if o, err := r.vm.New(ctor, r.vm.ToValue(msg)); err == nil {
			return o
		}
	}
	return r.vm.NewGoError(errors.New(msg))
}

// jsError maps a Go error from an op to a JS exception value.
func (r *Runtime) jsError(err error) goja.Value {
	var pe *PermissionError
	switch {
	case errors.As(err, &pe):
		return r.newError("PermissionDenied", pe.Error())
	case errors.Is(err, os.ErrNotExist):
		return r.newError("NotFound", err.Error())
	case errors.Is(err, ErrTimeout):
		return r.newError("TimedOut", err.Error())
	}
	return r.vm.NewGoError(err)
}

// throw raises err as a JS exception from inside a native function.
func (r *Runtime) throw(err error) {
	panic(r.jsError(err))
}

// RunFile executes the main script from the real filesystem, then the loop.
func (r *Runtime) RunFile(path string) error {
	// The entry script itself is implicitly readable (like Deno's main module);
	// everything it loads afterwards goes through --allow-read.
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if abs, err := filepath.Abs(path); err == nil {
		r.mainDir = filepath.Dir(abs)
	}
	return r.RunSource(path, string(src))
}

// RunSource runs src as the main script (name is used in stack traces; if
// it is an absolute path, top-level require() resolves relative to it).
func (r *Runtime) RunSource(name, src string) error {
	if err := r.Load(name, src); err != nil {
		return err
	}
	if r.opts.VirtualClock {
		for {
			done, err := r.Step()
			if done {
				return err
			}
		}
	}
	if r.opts.Timeout > 0 {
		// Resource limit #2: wall clock. vm.Interrupt is the only goroutine-
		// safe method on goja.Runtime; it sets a flag checked by the
		// interpreter loop, so it also breaks `while(true){}`.
		t := time.AfterFunc(r.opts.Timeout, func() { r.loop.Stop(ErrTimeout) })
		defer t.Stop()
	}
	return r.loop.Run(r.loop.main)
}

// Load compiles the main script and arms it as the first task, without
// running anything. Use Step (virtual clock) or RunSource.
func (r *Runtime) Load(name, src string) error {
	if r.mainDir == "" && filepath.IsAbs(name) {
		r.mainDir = filepath.Dir(name)
	}
	if r.mainDir != "" {
		// Top-level require() resolves relative to the script, not the cwd.
		_ = r.vm.Set("require", r.makeRequire(r.mainDir))
	}
	if r.trace != nil {
		_ = r.vm.Set(mainEndName, func() {
			_ = r.vm.GlobalObject().Delete(mainEndName)
			r.bodyEnd()
		})
		src += "\n;" + mainEndName + "();"
	}
	prog, err := goja.Compile(name, src, false)
	if err != nil {
		if r.trace != nil {
			r.trace.Emit(Event{Type: EvExit, Err: err.Error()})
		}
		return err
	}
	r.loop.Start(func() error {
		_, err := r.vm.RunProgram(prog)
		return err
	})
	return nil
}

// Step runs one macrotask and its microtasks (virtual clock only). A
// per-step wall-clock watchdog (Options.Timeout) interrupts runaway JS such
// as `for(;;){}` where the runtime can preempt it (not under js/wasm, where
// nothing runs concurrently: the browser page terminates its Worker).
func (r *Runtime) Step() (done bool, err error) {
	if !r.opts.VirtualClock {
		return true, errors.New("Step requires Options.VirtualClock")
	}
	if fin, ferr := r.loop.Finished(); fin {
		return true, ferr
	}
	if r.opts.Timeout > 0 {
		t := time.AfterFunc(r.opts.Timeout, func() { r.loop.Stop(ErrTimeout) })
		defer t.Stop()
	}
	done, err = r.loop.Step()
	if done && r.trace != nil {
		e := Event{Type: EvExit}
		if err != nil {
			e.Err = FormatError(err)
			e.Label = fmt.Sprint(ExitCode(err))
		}
		r.trace.Emit(e)
	}
	return done, err
}

// Snapshot returns the loop state (call between steps).
func (r *Runtime) Snapshot() LoopSnapshot { return r.loop.Snapshot() }

// Stop interrupts the runtime from any goroutine.
func (r *Runtime) Stop(reason error) { r.loop.Stop(reason) }

// ExitCode maps a run result to the CLI exit code.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrTimeout):
		return 124
	}
	return 1
}

// FormatError renders an error returned from RunFile for the terminal.
func FormatError(err error) string {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		if o, ok := ex.Value().(*goja.Object); ok {
			if s := o.Get("stack"); s != nil && !goja.IsUndefined(s) {
				return "Uncaught " + strings.TrimRight(s.String(), "\n")
			}
		}
		return "Uncaught " + ex.String()
	}
	var so *goja.StackOverflowError
	if errors.As(err, &so) {
		return "RangeError: maximum call stack size exceeded"
	}
	return "error: " + err.Error()
}

// --- permission checks with tracing ---

func (r *Runtime) tracePerm(kind, target string, canonical string, err error) {
	if r.trace == nil {
		return
	}
	e := Event{Type: EvPerm, Kind: kind, Target: target, Path: canonical, Allowed: err == nil}
	var pe *PermissionError
	if errors.As(err, &pe) {
		e.Flag = pe.Flag
		e.Path = pe.Resolved
		if e.Path == "" && (kind == "read" || kind == "write") {
			// Nothing granted, so the check never resolved the path. For
			// the host-side trace only (never shown to the script), show
			// where it would have pointed.
			if p, rerr := ResolvePathFS(r.fs, target); rerr == nil {
				e.Path = p
			}
		}
	}
	r.trace.Emit(e)
}

func (r *Runtime) checkRead(path string) (string, error) {
	c, err := r.perms.CheckRead(path)
	r.tracePerm("read", path, c, err)
	return c, err
}

func (r *Runtime) checkWrite(path string) (string, error) {
	c, err := r.perms.CheckWrite(path)
	r.tracePerm("write", path, c, err)
	return c, err
}

func (r *Runtime) checkNet(host string) error {
	err := r.perms.CheckNet(host)
	r.tracePerm("net", host, "", err)
	return err
}

func (r *Runtime) checkEnv(name string) error {
	err := r.perms.CheckEnv(name)
	r.tracePerm("env", name, "", err)
	return err
}
