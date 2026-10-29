//go:build js && wasm

// web/os 用の WebAssembly エントリポイント。JS に globalThis.rvemu を公開する。
//
//	GOOS=js GOARCH=wasm go build -o web/os/main.wasm ./cmd/wasm
package main

import "syscall/js"

func main() {
	s := &session{}
	api := map[string]any{
		// load(Uint8Array kernel, ramMiB) → エラーメッセージ (成功なら "")
		"load": js.FuncOf(func(this js.Value, a []js.Value) any {
			buf := make([]byte, a[0].Get("length").Int())
			js.CopyBytesToGo(buf, a[0])
			if err := s.load(buf, a[1].Int()); err != nil {
				return err.Error()
			}
			return ""
		}),
		"reset": js.FuncOf(func(this js.Value, a []js.Value) any {
			if err := s.reset(); err != nil {
				return err.Error()
			}
			return ""
		}),
		// run(maxSteps, idleTicks, untilTrap) → runResult の JSON
		"run": js.FuncOf(func(this js.Value, a []js.Value) any {
			return s.run(uint64(a[0].Float()), uint64(a[1].Float()), a[2].Bool())
		}),
		"input": js.FuncOf(func(this js.Value, a []js.Value) any { s.input(a[0].String()); return nil }),
		"state": js.FuncOf(func(this js.Value, a []js.Value) any { return s.state() }),
		"disasm": js.FuncOf(func(this js.Value, a []js.Value) any {
			return s.disasm(a[0].String(), a[1].Int(), a[2].Int())
		}),
		"trace":  js.FuncOf(func(this js.Value, a []js.Value) any { return s.trace() }),
		"traps":  js.FuncOf(func(this js.Value, a []js.Value) any { return s.traps(a[0].Int()) }),
		"mem":    js.FuncOf(func(this js.Value, a []js.Value) any { return s.mem(a[0].String(), a[1].Int()) }),
		"symbol": js.FuncOf(func(this js.Value, a []js.Value) any { return s.symbol(a[0].String()) }),
	}
	js.Global().Set("rvemu", js.ValueOf(api))
	select {} // JS から呼ばれ続けるので終了しない
}
