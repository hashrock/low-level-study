//go:build js && wasm

// web/pipeline 用の WebAssembly エントリポイント。JS に globalThis.pipeline を公開する。
//
//	GOOS=js GOARCH=wasm go build -o ../main.wasm .
package main

import "syscall/js"

func main() {
	s := newSession()
	str := func(f func(a []js.Value) string) js.Func {
		return js.FuncOf(func(this js.Value, a []js.Value) any { return f(a) })
	}
	api := map[string]any{
		// compile({lang, name, src, opt} の JSON) → compileResult の JSON
		"compile": str(func(a []js.Value) string { return s.compile(a[0].String()) }),
		// start(args, stdin, budget) → エラーメッセージ (成功なら "")
		"start":    str(func(a []js.Value) string { return s.start(a[0].String(), a[1].String(), uint64(a[2].Float())) }),
		"run":      str(func(a []js.Value) string { return s.run(uint64(a[0].Float())) }),
		"stepLine": str(func(a []js.Value) string { return s.stepLine(uint64(a[0].Float())) }),
		"state":    str(func(a []js.Value) string { return s.state() }),
		"syscalls": str(func(a []js.Value) string { return s.syscalls(a[0].Int()) }),
		// benchStart(lang, name, src, opt) → {static, func, error}
		"benchStart": str(func(a []js.Value) string {
			return s.benchStart(a[0].String(), a[1].String(), a[2].String(), a[3].Int())
		}),
		"benchRun": str(func(a []js.Value) string { return s.benchRun(uint64(a[0].Float())) }),
	}
	js.Global().Set("pipeline", js.ValueOf(api))
	select {}
}
