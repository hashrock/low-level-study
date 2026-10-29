package main

// console.go — console.log & friends, plus a small value inspector.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

func (r *Runtime) installConsole() {
	console := r.vm.NewObject()
	out := func(toErr bool) func(goja.FunctionCall) goja.Value {
		return func(c goja.FunctionCall) goja.Value {
			parts := make([]string, len(c.Arguments))
			for i, a := range c.Arguments {
				parts[i] = describeTop(a)
			}
			line := strings.Join(parts, " ") + "\n"
			if toErr {
				fmt.Fprint(r.opts.Stderr, line)
			} else {
				fmt.Fprint(r.opts.Stdout, line)
			}
			return goja.Undefined()
		}
	}
	for _, n := range []string{"log", "info", "debug"} {
		_ = console.Set(n, out(false))
	}
	for _, n := range []string{"error", "warn"} {
		_ = console.Set(n, out(true))
	}
	_ = r.vm.Set("console", console)
}

// describeTop prints top-level strings raw (like console.log("hi")).
func describeTop(v goja.Value) string {
	if _, isObj := v.(*goja.Object); !isObj && v != nil {
		if s, ok := v.Export().(string); ok {
			return s
		}
	}
	return describeValue(v)
}

func describeValue(v goja.Value) string {
	var b strings.Builder
	inspect(&b, v, 0, map[*goja.Object]bool{})
	return b.String()
}

func inspect(b *strings.Builder, v goja.Value, depth int, seen map[*goja.Object]bool) {
	if v == nil || goja.IsUndefined(v) {
		b.WriteString("undefined")
		return
	}
	if goja.IsNull(v) {
		b.WriteString("null")
		return
	}
	obj, isObj := v.(*goja.Object)
	if !isObj {
		if _, ok := v.Export().(string); ok {
			b.WriteString(strconv.Quote(v.String()))
		} else {
			b.WriteString(v.String())
		}
		return
	}
	if seen[obj] {
		b.WriteString("[Circular]")
		return
	}
	switch obj.ClassName() {
	case "Function":
		name := obj.Get("name")
		if name == nil || name.String() == "" {
			b.WriteString("[Function (anonymous)]")
		} else {
			b.WriteString("[Function: " + name.String() + "]")
		}
		return
	case "Error":
		if s := obj.Get("stack"); s != nil && !goja.IsUndefined(s) {
			b.WriteString(strings.TrimRight(s.String(), "\n"))
		} else {
			b.WriteString(obj.String())
		}
		return
	case "Promise":
		b.WriteString("Promise {}")
		return
	case "Date", "RegExp":
		b.WriteString(obj.String())
		return
	}
	if depth > 3 {
		b.WriteString("[Object]")
		return
	}
	seen[obj] = true
	defer delete(seen, obj)
	keys := obj.Keys()
	if obj.ClassName() == "Array" {
		b.WriteString("[")
		n := int(obj.Get("length").ToInteger())
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			if i >= 100 {
				fmt.Fprintf(b, "... %d more", n-i)
				break
			}
			inspect(b, obj.Get(strconv.Itoa(i)), depth+1, seen)
		}
		b.WriteString("]")
		return
	}
	if len(keys) == 0 {
		b.WriteString("{}")
		return
	}
	b.WriteString("{ ")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k + ": ")
		inspect(b, obj.Get(k), depth+1, seen)
	}
	b.WriteString(" }")
}
