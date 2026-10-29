package main

// ops_net.go — a minimal fetch(url, {method, body, headers}).
//
// Only http/https. The permission check runs on the loop goroutine before
// any socket is opened, and again for *every redirect hop* (otherwise an
// allowed host could 302 us to an internal address).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const MaxResponseBytes = 16 << 20

type fetchResult struct {
	status     int
	statusText string
	url        string
	headers    map[string]string
	body       string
}

func (r *Runtime) httpClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		// Note: http.Client.CheckRedirect runs on a worker goroutine. It only
		// reads the immutable Permissions value, never goja state.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return r.perms.CheckNet(req.URL.Host)
		},
	}
}

func (r *Runtime) installNet() {
	client := r.httpClient()
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
		if err := r.perms.CheckNet(u.Host); err != nil {
			return fail(err)
		}
		// Read options now, on the loop goroutine; the worker gets Go values only.
		method, body, headers := "GET", "", map[string]string{}
		if o, ok := c.Argument(1).(*goja.Object); ok {
			if m := o.Get("method"); m != nil && !goja.IsUndefined(m) {
				method = strings.ToUpper(m.String())
			}
			if b := o.Get("body"); b != nil && !goja.IsUndefined(b) {
				body = b.String()
			}
			if h, ok := o.Get("headers").(*goja.Object); ok {
				for _, k := range h.Keys() {
					headers[k] = h.Get(k).String()
				}
			}
		}
		target := u.String()

		return r.loop.PromiseOp(func(ctx context.Context) (interface{}, error) {
			req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
			if err != nil {
				return nil, err
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
			if err != nil {
				return nil, err
			}
			h := map[string]string{}
			for k := range resp.Header {
				h[strings.ToLower(k)] = resp.Header.Get(k)
			}
			return &fetchResult{resp.StatusCode, resp.Status, resp.Request.URL.String(), h, string(data)}, nil
		}, func(res interface{}) goja.Value {
			return r.responseObject(res.(*fetchResult))
		}, r.jsError)
	})
}

// responseObject builds the JS Response-like object (loop goroutine only).
func (r *Runtime) responseObject(fr *fetchResult) goja.Value {
	o := r.vm.NewObject()
	_ = o.Set("status", fr.status)
	_ = o.Set("statusText", fr.statusText)
	_ = o.Set("ok", fr.status >= 200 && fr.status < 300)
	_ = o.Set("url", fr.url)
	hdr := r.vm.NewObject()
	for k, v := range fr.headers {
		_ = hdr.Set(k, v)
	}
	_ = o.Set("headers", hdr)
	resolved := func(v goja.Value) goja.Value {
		p, resolve, _ := r.vm.NewPromise()
		_ = resolve(v)
		return r.vm.ToValue(p)
	}
	_ = o.Set("text", func() goja.Value { return resolved(r.vm.ToValue(fr.body)) })
	// json() is plain JS on top of text(): no reason to do it in Go.
	_ = o.Set("json", r.jsonMethod)
	return o
}
