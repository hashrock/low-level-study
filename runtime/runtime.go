package main

// runtime.go — wires the engine (goja) to our runtime layer.
//
// Engine responsibilities (goja): parsing, bytecode, GC, Promise/async-await
// semantics, the microtask (job) queue, built-ins like JSON/Math/Array.
//
// Runtime responsibilities (this package): which globals exist at all,
// the event loop and timers, async host ops on goroutines, permissions,
// resource limits, error reporting and exit codes.

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
	Timeout      time.Duration // 0 = no wall-clock limit
	MaxCallStack int           // max JS call depth
	Args         []string      // exposed as Sandbox.args
	Stdout       io.Writer
	Stderr       io.Writer
}

type Runtime struct {
	vm    *goja.Runtime
	loop  *Loop
	perms *Permissions
	opts  Options

	errCtors   map[string]goja.Value // Sandbox.errors.*
	rejections map[*goja.Promise]struct{}
	modules    map[string]*goja.Object // require() cache, keyed by canonical path
	jsonMethod goja.Value              // shared Response.prototype.json-like function
}

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
	vm := goja.New()
	// Resource limit #1: call depth. goja raises an uncatchable
	// *goja.StackOverflowError when exceeded, so a script can't swallow it.
	vm.SetMaxCallStackSize(opts.MaxCallStack)
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())

	r := &Runtime{
		vm:         vm,
		loop:       NewLoop(vm),
		perms:      &opts.Perms,
		opts:       opts,
		errCtors:   map[string]goja.Value{},
		rejections: map[*goja.Promise]struct{}{},
		modules:    map[string]*goja.Object{},
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
	r.loop.afterTask = r.checkRejections

	sandbox := vm.NewObject()
	if err := vm.Set("Sandbox", sandbox); err != nil {
		return nil, err
	}
	if _, err := vm.RunString(bootstrapJS); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
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

// RunFile executes the main script, then the event loop.
func (r *Runtime) RunFile(path string) error {
	// The entry script itself is implicitly readable (like Deno's main module);
	// everything it loads afterwards goes through --allow-read.
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if abs, err := filepath.Abs(path); err == nil {
		// Top-level require() resolves relative to the script, not the cwd.
		_ = r.vm.Set("require", r.makeRequire(filepath.Dir(abs)))
	}
	return r.RunSource(path, string(src))
}

func (r *Runtime) RunSource(name, src string) error {
	if r.opts.Timeout > 0 {
		// Resource limit #2: wall clock. vm.Interrupt is the only goroutine-
		// safe method on goja.Runtime; it sets a flag checked by the
		// interpreter loop, so it also breaks `while(true){}`.
		t := time.AfterFunc(r.opts.Timeout, func() { r.loop.Stop(ErrTimeout) })
		defer t.Stop()
	}
	prog, err := goja.Compile(name, src, false)
	if err != nil {
		return err
	}
	return r.loop.Run(func() error {
		_, err := r.vm.RunProgram(prog)
		return err
	})
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
