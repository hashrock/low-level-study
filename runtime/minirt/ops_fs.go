package minirt

// ops_fs.go — Sandbox.readTextFile / writeTextFile (+ Sync variants).
//
// Pattern for every op:
//   1. On the loop goroutine: validate args, run the permission check, get
//      the canonical path. Permission errors reject/throw immediately.
//   2. Async variant: do the I/O off the JS thread (Loop.PromiseOp),
//      resolve the Promise back on the loop goroutine.
//   Sync variant: do the I/O right here, blocking the whole loop — which
//   is precisely why the async variant exists.

import (
	"context"

	"github.com/dop251/goja"
)

// MaxReadBytes caps a single read so one call can't pull a huge file into
// Go memory. (This is not a general memory limit — see README.)
const MaxReadBytes = 64 << 20

func (r *Runtime) installFS(sandbox *goja.Object) {
	// rejected returns an already-rejected promise (async ops never throw
	// synchronously, matching how fetch/Deno APIs behave).
	rejected := func(err error) goja.Value {
		p, _, reject := r.vm.NewPromise()
		_ = reject(r.jsError(err))
		return r.vm.ToValue(p)
	}
	_ = sandbox.Set("readTextFile", func(path string) goja.Value {
		real, err := r.checkRead(path)
		if err != nil {
			return rejected(err)
		}
		return r.loop.PromiseOp(&Op{Kind: "read", Target: real},
			func(ctx context.Context, op *Op) (interface{}, error) {
				op.Latency = r.fs.Latency()
				return r.fs.ReadFile(real, MaxReadBytes)
			},
			func(res interface{}) goja.Value { return r.vm.ToValue(res) },
			r.jsError)
	})

	_ = sandbox.Set("writeTextFile", func(path, data string) goja.Value {
		real, err := r.checkWrite(path)
		if err != nil {
			return rejected(err)
		}
		return r.loop.PromiseOp(&Op{Kind: "write", Target: real},
			func(ctx context.Context, op *Op) (interface{}, error) {
				op.Latency = r.fs.Latency()
				return nil, r.fs.WriteFile(real, data)
			},
			func(interface{}) goja.Value { return goja.Undefined() },
			r.jsError)
	})

	_ = sandbox.Set("readTextFileSync", func(path string) string {
		real, err := r.checkRead(path)
		if err != nil {
			r.throw(err)
		}
		s, err := r.fs.ReadFile(real, MaxReadBytes)
		if err != nil {
			r.throw(err)
		}
		return s
	})

	_ = sandbox.Set("writeTextFileSync", func(path, data string) {
		real, err := r.checkWrite(path)
		if err != nil {
			r.throw(err)
		}
		if err := r.fs.WriteFile(real, data); err != nil {
			r.throw(err)
		}
	})
}
