package minirt

// ops_timers.go — setTimeout / setInterval / clearTimeout / clearInterval.

import (
	"time"

	"github.com/dop251/goja"
)

func (r *Runtime) installTimers() {
	add := func(repeat bool) func(goja.FunctionCall) goja.Value {
		name := "setTimeout"
		if repeat {
			name = "setInterval"
		}
		return func(c goja.FunctionCall) goja.Value {
			if _, ok := goja.AssertFunction(c.Argument(0)); !ok {
				panic(r.vm.NewTypeError("callback must be a function"))
			}
			ms := c.Argument(1).ToFloat()
			if ms != ms || ms < 0 { // NaN or negative
				ms = 0
			}
			var args []goja.Value
			if len(c.Arguments) > 2 {
				args = append(args, c.Arguments[2:]...)
			}
			label := ""
			if r.trace != nil {
				label = name + "(" + funcName(c.Argument(0)) + ")"
			}
			id := r.loop.AddTimer(c.Argument(0), time.Duration(ms*float64(time.Millisecond)), repeat, args, label)
			return r.vm.ToValue(id)
		}
	}
	clear := func(c goja.FunctionCall) goja.Value {
		r.loop.ClearTimer(c.Argument(0).ToInteger())
		return goja.Undefined()
	}
	_ = r.vm.Set("setTimeout", add(false))
	_ = r.vm.Set("setInterval", add(true))
	_ = r.vm.Set("clearTimeout", clear)
	_ = r.vm.Set("clearInterval", clear)
}
