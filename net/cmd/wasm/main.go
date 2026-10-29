//go:build js && wasm

// ブラウザ版 mininet: 2 つのスタックを 1 つの wasm の中で動かし、JS から観察・操作する。
//
//	mininet.reset()                   セッションを作り直す
//	mininet.poll(since) -> JSON       since 番目以降のイベント + 両ノードのスナップショット (sim.PollResult)
//	mininet.frame(id)   -> JSON       フレームの層ごとの分解と 16 進 (sim.FrameDetail)
//	mininet.run(name, arg) -> opId    ping / udp / tcp-echo / bulk / http / rst / flush-arp (結果は op イベント)
//	mininet.setLoss(p) / setLatency(ms) / dropNext() / setPaused(bool) / step() / dropHead()
//	mininet.pcap(includeDropped) -> Uint8Array
//
// JS から呼ばれる関数はブロックしない: 時間のかかる処理はゴルーチンで走らせ、結果はイベントで返す。
package main

import (
	"sync"
	"syscall/js"
	"time"

	"mininet/sim"
)

var (
	mu  sync.Mutex
	cur *sim.Session
)

func session() *sim.Session {
	mu.Lock()
	defer mu.Unlock()
	return cur
}

func fn(f func(args []js.Value) any) js.Func {
	return js.FuncOf(func(this js.Value, args []js.Value) any { return f(args) })
}

func arg(args []js.Value, i int) js.Value {
	if i < len(args) {
		return args[i]
	}
	return js.Undefined()
}

func main() {
	cur = sim.New()
	api := js.Global().Get("Object").New()
	api.Set("reset", fn(func(args []js.Value) any {
		mu.Lock()
		old := cur
		cur = sim.New()
		mu.Unlock()
		go old.Close() // Close は受信ゴルーチンの終了を待つので JS のスレッドでは呼ばない
		return nil
	}))
	api.Set("poll", fn(func(args []js.Value) any {
		since := 0
		if a := arg(args, 0); a.Type() == js.TypeNumber {
			since = a.Int()
		}
		return sim.JSON(session().Poll(since, 3000))
	}))
	api.Set("frame", fn(func(args []js.Value) any {
		d, ok := session().Frame(uint64(arg(args, 0).Int()))
		if !ok {
			return nil
		}
		return sim.JSON(d)
	}))
	api.Set("run", fn(func(args []js.Value) any {
		a := ""
		if v := arg(args, 1); v.Type() == js.TypeString {
			a = v.String()
		}
		return session().Run(arg(args, 0).String(), a)
	}))
	api.Set("setLoss", fn(func(args []js.Value) any { session().SetLoss(arg(args, 0).Float()); return nil }))
	api.Set("setLatency", fn(func(args []js.Value) any {
		session().SetLatency(time.Duration(arg(args, 0).Float() * float64(time.Millisecond)))
		return nil
	}))
	api.Set("dropNext", fn(func(args []js.Value) any { session().DropNext(); return nil }))
	api.Set("setPaused", fn(func(args []js.Value) any { session().SetPaused(arg(args, 0).Truthy()); return nil }))
	api.Set("step", fn(func(args []js.Value) any { go session().Step(); return nil }))
	api.Set("dropHead", fn(func(args []js.Value) any { go session().DropHead(); return nil }))
	api.Set("pcap", fn(func(args []js.Value) any {
		b := session().PCAP(arg(args, 0).Truthy())
		u := js.Global().Get("Uint8Array").New(len(b))
		js.CopyBytesToJS(u, b)
		return u
	}))
	api.Set("ready", true)
	js.Global().Set("mininet", api)
	select {}
}
