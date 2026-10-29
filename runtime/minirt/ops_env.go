package minirt

// ops_env.go — Sandbox.env.get / has, gated by --allow-env.

import (
	"github.com/dop251/goja"
)

func (r *Runtime) installEnv(sandbox *goja.Object) {
	env := r.vm.NewObject()
	_ = env.Set("get", func(name string) goja.Value {
		if err := r.checkEnv(name); err != nil {
			r.throw(err)
		}
		v, ok := r.opts.LookupEnv(name)
		if !ok {
			return goja.Undefined()
		}
		return r.vm.ToValue(v)
	})
	_ = env.Set("has", func(name string) bool {
		if err := r.checkEnv(name); err != nil {
			r.throw(err)
		}
		_, ok := r.opts.LookupEnv(name)
		return ok
	})
	_ = sandbox.Set("env", env)
}
