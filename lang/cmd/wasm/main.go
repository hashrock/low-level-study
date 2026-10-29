//go:build js && wasm

// ブラウザ版 minilang (web/lang/)。JS へ globalThis.minilang として小さな API を公開する。
// 値はすべて JSON 文字列で返す。ロジック本体は minilang.Session (通常の go test で検証済み)。
//
//	minilang.examples()          → [{name, src, out}]
//	minilang.load(src)           → {ok, error?, protos?}   コンパイルして実行準備
//	minilang.step(mode, budget)  → {status, steps, out, error?}  mode: instr | line | out | run
//	minilang.snapshot()          → VM の状態 (スタック・フレーム・グローバル・クロージャ・セル)
//	minilang.reset()             → {ok}  同じプログラムを最初から
package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	"minilang/examples"
	"minilang/minilang"
)

var sess *minilang.Session

func toJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"ok": false, "error": map[string]any{"text": err.Error(), "msg": err.Error()}})
	}
	return string(b)
}

func fn(f func(args []js.Value) any) js.Func {
	return js.FuncOf(func(this js.Value, args []js.Value) (res any) {
		defer func() {
			if r := recover(); r != nil { // Go 側の想定外の panic も画面に出せるように
				msg := fmt.Sprint(r)
				res = toJSON(map[string]any{"ok": false, "status": "error", "error": map[string]any{"kind": "internal error", "msg": msg, "text": "internal error: " + msg}})
			}
		}()
		return f(args)
	})
}

func main() {
	api := map[string]any{
		"examples": fn(func(args []js.Value) any { return toJSON(examples.All()) }),
		"load": fn(func(args []js.Value) any {
			s, err := minilang.Compile(args[0].String())
			if err != nil {
				sess = nil
				return toJSON(map[string]any{"ok": false, "error": minilang.ErrorInfo(err)})
			}
			sess = s
			return toJSON(map[string]any{"ok": true, "protos": s.Disassembly()})
		}),
		"step": fn(func(args []js.Value) any {
			if sess == nil {
				return toJSON(map[string]any{"status": "error", "error": map[string]any{"msg": "no program", "text": "no program"}})
			}
			return toJSON(sess.Step(args[0].String(), args[1].Int()))
		}),
		"snapshot": fn(func(args []js.Value) any {
			if sess == nil {
				return "null"
			}
			return toJSON(sess.VM.Snapshot())
		}),
		"reset": fn(func(args []js.Value) any {
			if sess != nil {
				sess.Reset()
			}
			return toJSON(map[string]any{"ok": sess != nil})
		}),
	}
	js.Global().Set("minilang", js.ValueOf(api))
	select {}
}
