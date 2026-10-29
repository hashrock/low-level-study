package main

// ops_env.go — Sandbox.env.get / has, gated by --allow-env.

import (
	"os"

	"github.com/dop251/goja"
)

func (r *Runtime) installEnv(sandbox *goja.Object) {
	env := r.vm.NewObject()
	_ = env.Set("get", func(name string) goja.Value {
		if err := r.perms.CheckEnv(name); err != nil {
			r.throw(err)
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			return goja.Undefined()
		}
		return r.vm.ToValue(v)
	})
	_ = env.Set("has", func(name string) bool {
		if err := r.perms.CheckEnv(name); err != nil {
			r.throw(err)
		}
		_, ok := os.LookupEnv(name)
		return ok
	})
	_ = sandbox.Set("env", env)
}
