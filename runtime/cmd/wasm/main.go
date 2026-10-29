//go:build js && wasm

// Command wasm exposes the minirt playground to JavaScript as globalThis.minirt.
// The page runs it inside a Web Worker: a script like `for(;;){}` can only be
// stopped by terminating the worker, since nothing preempts goja under
// js/wasm.
//
//	minirt.examples()            -> JSON {name: source}
//	minirt.defaults()            -> JSON {cwd, routes, env, files}
//	minirt.load(cfgJSON, onEvent)-> JSON {ok, error, flags}   onEvent(eventJSON) per trace event
//	minirt.step()                -> JSON StepResult (one task + its microtasks)
//	minirt.run(maxSteps)         -> JSON StepResult
//	minirt.files()               -> JSON [MemEntry]
package main

import (
	"encoding/json"
	"syscall/js"

	"minirt/minirt"
	"minirt/playground"
)

// maxEvents caps the trace of one run so a long setInterval can't flood the page.
const maxEvents = 50000

var session *playground.Session

func main() {
	api := js.Global().Get("Object").New()
	api.Set("examples", js.FuncOf(func(js.Value, []js.Value) any {
		return playground.MustJSON(playground.Examples())
	}))
	api.Set("defaults", js.FuncOf(func(js.Value, []js.Value) any {
		return playground.MustJSON(map[string]any{
			"cwd":    playground.Cwd,
			"routes": minirt.DefaultFakeRoutes(),
			"env":    playground.DefaultEnv(),
			"files":  playground.NewFS(0).List(4096),
		})
	}))
	api.Set("load", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var cfg playground.Config
		if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
			return playground.MustJSON(map[string]any{"ok": false, "error": err.Error()})
		}
		onEvent := args[1]
		n := 0
		emit := func(e minirt.Event) {
			n++
			if n > maxEvents && e.Type != minirt.EvExit {
				if n == maxEvents+1 {
					onEvent.Invoke(`{"type":"truncated"}`)
				}
				return
			}
			b, _ := json.Marshal(e)
			onEvent.Invoke(string(b))
		}
		s, err := playground.NewSession(cfg, emit)
		if err != nil {
			session = nil
			return playground.MustJSON(map[string]any{"ok": false, "error": err.Error()})
		}
		session = s
		return playground.MustJSON(map[string]any{"ok": true, "flags": cfg.Flags()})
	}))
	api.Set("step", js.FuncOf(func(js.Value, []js.Value) any {
		if session == nil {
			return `{"done":true,"error":"no script loaded"}`
		}
		return playground.MustJSON(session.Step())
	}))
	api.Set("run", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if session == nil {
			return `{"done":true,"error":"no script loaded"}`
		}
		max := 1000
		if len(args) > 0 && args[0].Type() == js.TypeNumber {
			max = args[0].Int()
		}
		return playground.MustJSON(session.Run(max))
	}))
	api.Set("files", js.FuncOf(func(js.Value, []js.Value) any {
		if session == nil {
			return "[]"
		}
		return playground.MustJSON(session.Files())
	}))
	js.Global().Set("minirt", api)
	select {}
}
