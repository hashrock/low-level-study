package minirt

// ops_net.go — a minimal fetch(url, {method, body, headers}).
//
// Only http/https. The permission check runs on the loop goroutine before
// any request is made, and again for *every redirect hop* (otherwise an
// allowed host could 302 us to an internal address). The transport is
// pluggable (Options.Network): real HTTP for the CLI, canned responses in
// the browser build.

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/dop251/goja"
)

const MaxResponseBytes = 16 << 20

func (r *Runtime) installNet() {
	r.jsonMethod, _ = r.vm.RunString("(function json() { return this.text().then(JSON.parse); })")
	_ = r.vm.Set("fetch", func(c goja.FunctionCall) goja.Value {
		p, _, reject := r.vm.NewPromise()
		fail := func(err error) goja.Value { _ = reject(r.jsError(err)); return r.vm.ToValue(p) }

		u, err := url.Parse(c.Argument(0).String())
		if err != nil {
			return fail(err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			_ = reject(r.vm.NewTypeError(fmt.Sprintf("fetch: unsupported scheme %q", u.Scheme)))
			return r.vm.ToValue(p)
		}
		if err := r.checkNet(u.Host); err != nil {
			return fail(err)
		}
		// Read options now, on the loop goroutine; the worker gets Go values only.
		req := FetchRequest{Method: "GET", URL: u.String(), Headers: map[string]string{}}
		if o, ok := c.Argument(1).(*goja.Object); ok {
			if m := o.Get("method"); m != nil && !goja.IsUndefined(m) {
				req.Method = strings.ToUpper(m.String())
			}
			if b := o.Get("body"); b != nil && !goja.IsUndefined(b) {
				req.Body = b.String()
			}
			if h, ok := o.Get("headers").(*goja.Object); ok {
				for _, k := range h.Keys() {
					req.Headers[k] = h.Get(k).String()
				}
			}
		}
		return r.loop.PromiseOp(&Op{Kind: "fetch", Target: req.Method + " " + req.URL},
			func(ctx context.Context, op *Op) (interface{}, error) {
				// checkNet only reads immutable permission data and the
				// goroutine-safe tracer, so it is fine on a worker.
				resp, err := r.net.Fetch(ctx, req, r.checkNet)
				if resp != nil {
					op.Latency = resp.Latency
				}
				if err != nil {
					return nil, err
				}
				return resp, nil
			}, func(res interface{}) goja.Value {
				return r.responseObject(res.(*FetchResponse))
			}, r.jsError)
	})
}

// responseObject builds the JS Response-like object (loop goroutine only).
func (r *Runtime) responseObject(fr *FetchResponse) goja.Value {
	o := r.vm.NewObject()
	_ = o.Set("status", fr.Status)
	_ = o.Set("statusText", fr.StatusText)
	_ = o.Set("ok", fr.Status >= 200 && fr.Status < 300)
	_ = o.Set("url", fr.URL)
	hdr := r.vm.NewObject()
	for k, v := range fr.Headers {
		_ = hdr.Set(k, v)
	}
	_ = o.Set("headers", hdr)
	resolved := func(v goja.Value) goja.Value {
		p, resolve, _ := r.vm.NewPromise()
		_ = resolve(v)
		return r.vm.ToValue(p)
	}
	_ = o.Set("text", func() goja.Value { return resolved(r.vm.ToValue(fr.Body)) })
	// json() is plain JS on top of text(): no reason to do it in Go.
	_ = o.Set("json", r.jsonMethod)
	return o
}
