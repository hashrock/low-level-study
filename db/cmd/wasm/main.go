//go:build js && wasm

// ブラウザ版 minidb。メモリ上のファイル (MemFile) に DB を置き、
// JS に小さな API (globalThis.minidb) を公開する。関数はどれも JSON 文字列を返す。
package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	"minidb/minidb"
)

var db *minidb.DB

type result struct {
	Columns []string   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	Nulls   [][]bool   `json:"nulls,omitempty"`
	Message string     `json:"message"`
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"error": err.Error()})
	}
	return string(b)
}

func errJSON(err error) string { return toJSON(map[string]string{"error": err.Error()}) }

// wrap は panic を JSON のエラーにして返す関数を作る。
func wrap(f func(args []js.Value) string) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) (ret any) {
		defer func() {
			if r := recover(); r != nil {
				ret = errJSON(fmt.Errorf("panic: %v", r))
			}
		}()
		return f(args)
	})
}

func exec(args []js.Value) string {
	res, err := db.Exec(args[0].String())
	out := struct {
		Results []result            `json:"results"`
		Error   string              `json:"error,omitempty"`
		Stats   []*minidb.StmtStats `json:"stats"`
	}{Results: []result{}, Stats: db.LastStats()}
	for _, r := range res {
		rr := result{Columns: r.Columns, Message: r.Message}
		for _, row := range r.Rows {
			cells := make([]string, len(row))
			nulls := make([]bool, len(row))
			for i, v := range row {
				cells[i] = v.String()
				nulls[i] = v.Kind == minidb.KindNull
			}
			rr.Rows = append(rr.Rows, cells)
			rr.Nulls = append(rr.Nulls, nulls)
		}
		out.Results = append(out.Results, rr)
	}
	if err != nil {
		out.Error = err.Error()
	}
	return toJSON(out)
}

func main() {
	var err error
	if db, err = minidb.OpenMem(nil); err != nil {
		panic(err)
	}
	api := map[string]any{
		"exec":   wrap(exec),
		"tables": wrap(func([]js.Value) string { return toJSON(db.TableInfos()) }),
		"tree": wrap(func(a []js.Value) string {
			t, err := db.TreeDump(a[0].String())
			if err != nil {
				return errJSON(err)
			}
			return toJSON(t)
		}),
		"page": wrap(func(a []js.Value) string {
			p, err := db.PageInfo(uint32(a[0].Int()))
			if err != nil {
				return errJSON(err)
			}
			return toJSON(p)
		}),
		"cache": wrap(func([]js.Value) string { return toJSON(db.CacheState()) }),
		"setCacheCapacity": wrap(func(a []js.Value) string {
			if err := db.SetCacheCapacity(a[0].Int()); err != nil {
				return errJSON(err)
			}
			return toJSON(db.CacheState())
		}),
		"reset": wrap(func([]js.Value) string {
			cap := db.CacheState().Capacity
			db, _ = minidb.OpenMem(nil)
			db.SetCacheCapacity(cap)
			return "{}"
		}),
		// exportDB は DB ファイルの中身を Uint8Array で返す (CLI でそのまま開ける)。
		"exportDB": js.FuncOf(func(js.Value, []js.Value) any {
			data, err := db.Snapshot()
			if err != nil {
				return js.Null()
			}
			u8 := js.Global().Get("Uint8Array").New(len(data))
			js.CopyBytesToJS(u8, data)
			return u8
		}),
		// importDB は Uint8Array の DB ファイルを開く。成功なら "{}"。
		"importDB": wrap(func(a []js.Value) string {
			data := make([]byte, a[0].Get("length").Int())
			js.CopyBytesToGo(data, a[0])
			nd, err := minidb.OpenMem(data)
			if err != nil {
				return errJSON(err)
			}
			nd.SetCacheCapacity(db.CacheState().Capacity)
			db = nd
			return "{}"
		}),
	}
	obj := js.Global().Get("Object").New()
	for k, v := range api {
		obj.Set(k, v)
	}
	js.Global().Set("minidb", obj)
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("minidb-ready"))
	select {}
}
