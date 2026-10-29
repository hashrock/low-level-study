package main

// ops_fs.go — Sandbox.readTextFile / writeTextFile (+ Sync variants).
//
// Pattern for every op:
//   1. On the loop goroutine: validate args, run the permission check, get
//      the canonical path. Permission errors reject/throw immediately.
//   2. Async variant: do the syscall on a worker goroutine (Loop.PromiseOp),
//      resolve the Promise back on the loop goroutine.
//   Sync variant: do the syscall right here, blocking the whole loop — which
//   is precisely why the async variant exists.

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/dop251/goja"
)

// MaxReadBytes caps a single read so one call can't pull a huge file into
// Go memory. (This is not a general memory limit — see README.)
const MaxReadBytes = 64 << 20

func readFileLimited(path string) (string, error) {
	// O_NOFOLLOW: path is already canonical, so if the final component is a
	// symlink now, someone swapped it in after our check. Refuse.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxReadBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxReadBytes {
		return "", fmt.Errorf("%s: file larger than %d bytes", path, MaxReadBytes)
	}
	return string(data), nil
}

func writeFileNoFollow(path, data string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (r *Runtime) installFS(sandbox *goja.Object) {
	// rejected returns an already-rejected promise (async ops never throw
	// synchronously, matching how fetch/Deno APIs behave).
	rejected := func(err error) goja.Value {
		p, _, reject := r.vm.NewPromise()
		_ = reject(r.jsError(err))
		return r.vm.ToValue(p)
	}
	_ = sandbox.Set("readTextFile", func(path string) goja.Value {
		real, err := r.perms.CheckRead(path)
		if err != nil {
			return rejected(err)
		}
		return r.loop.PromiseOp(
			func(ctx context.Context) (interface{}, error) { return readFileLimited(real) },
			func(res interface{}) goja.Value { return r.vm.ToValue(res) },
			r.jsError)
	})

	_ = sandbox.Set("writeTextFile", func(path, data string) goja.Value {
		real, err := r.perms.CheckWrite(path)
		if err != nil {
			return rejected(err)
		}
		return r.loop.PromiseOp(
			func(ctx context.Context) (interface{}, error) { return nil, writeFileNoFollow(real, data) },
			func(interface{}) goja.Value { return goja.Undefined() },
			r.jsError)
	})

	_ = sandbox.Set("readTextFileSync", func(path string) string {
		real, err := r.perms.CheckRead(path)
		if err != nil {
			r.throw(err)
		}
		s, err := readFileLimited(real)
		if err != nil {
			r.throw(err)
		}
		return s
	})

	_ = sandbox.Set("writeTextFileSync", func(path, data string) {
		real, err := r.perms.CheckWrite(path)
		if err != nil {
			r.throw(err)
		}
		if err := writeFileNoFollow(real, data); err != nil {
			r.throw(err)
		}
	})
}
