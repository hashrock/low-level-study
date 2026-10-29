package minirt

// net.go — the network abstraction behind fetch().
//
// HTTPNetwork is the real thing (net/http). FakeNetwork serves canned
// responses with a configurable simulated latency; the browser build uses it
// (a web page cannot open arbitrary sockets, and a teaching page should not
// hit the internet anyway). Both re-check permissions on every redirect hop
// through the checkHop callback.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

type FetchRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

type FetchResponse struct {
	Status     int
	StatusText string
	URL        string // final URL after redirects
	Headers    map[string]string
	Body       string
	// Latency is the simulated time the request took (FakeNetwork). The
	// event loop delivers the response that much later. 0 for real HTTP.
	Latency time.Duration
}

// Network performs one fetch. checkHop must be called with the host[:port]
// of every redirect target before following it; a non-nil error aborts.
// Fetch may run on a worker goroutine: it must not touch goja.
type Network interface {
	Fetch(ctx context.Context, req FetchRequest, checkHop func(hostport string) error) (*FetchResponse, error)
}

// FakeRoute is one canned response.
type FakeRoute struct {
	URL         string            `json:"url"`
	Status      int               `json:"status"`
	ContentType string            `json:"contentType,omitempty"`
	Body        string            `json:"body,omitempty"`
	Location    string            `json:"location,omitempty"` // for 3xx
	DelayMs     float64           `json:"delayMs"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// FakeNetwork answers from a fixed route table. Unknown URLs fail like an
// unreachable host after DefaultDelay.
type FakeNetwork struct {
	mu           sync.RWMutex
	routes       map[string]FakeRoute
	DefaultDelay time.Duration
}

func NewFakeNetwork(routes []FakeRoute) *FakeNetwork {
	n := &FakeNetwork{routes: map[string]FakeRoute{}, DefaultDelay: 30 * time.Millisecond}
	for _, r := range routes {
		n.routes[normURL(r.URL)] = r
	}
	return n
}

func normURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

func (n *FakeNetwork) Fetch(ctx context.Context, fr FetchRequest, checkHop func(string) error) (*FetchResponse, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	cur := fr.URL
	var total time.Duration
	for hop := 0; ; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if hop >= 10 {
			return nil, fmt.Errorf("too many redirects")
		}
		r, ok := n.routes[normURL(cur)]
		if !ok {
			u, _ := url.Parse(cur)
			host := cur
			if u != nil {
				host = u.Host
			}
			return &FetchResponse{Latency: total + n.DefaultDelay},
				fmt.Errorf("fetch %s: dial tcp: lookup %s: no such host (fake network)", cur, host)
		}
		total += time.Duration(r.DelayMs * float64(time.Millisecond))
		if r.Status >= 300 && r.Status < 400 && r.Location != "" {
			next, err := url.Parse(cur)
			if err != nil {
				return nil, err
			}
			loc, err := next.Parse(r.Location)
			if err != nil {
				return nil, err
			}
			if err := checkHop(loc.Host); err != nil {
				return &FetchResponse{Latency: total}, err
			}
			cur = loc.String()
			continue
		}
		h := map[string]string{}
		for k, v := range r.Headers {
			h[strings.ToLower(k)] = v
		}
		if r.ContentType != "" {
			h["content-type"] = r.ContentType
		}
		status := r.Status
		if status == 0 {
			status = 200
		}
		return &FetchResponse{Status: status, StatusText: fmt.Sprintf("%d %s", status, statusText[status]),
			URL: cur, Headers: h, Body: r.Body, Latency: total}, nil
	}
}

// DefaultFakeRoutes is the route table used by the browser build.
func DefaultFakeRoutes() []FakeRoute {
	return []FakeRoute{
		{URL: "https://example.com/", Status: 200, ContentType: "text/html; charset=utf-8", DelayMs: 120,
			Body: "<!doctype html><html><head><title>Example Domain</title></head><body><h1>Example Domain</h1></body></html>\n"},
		{URL: "https://api.example.com/users/1", Status: 200, ContentType: "application/json", DelayMs: 80,
			Body: `{"id":1,"name":"Ada","langs":["go","js"]}`},
		{URL: "https://api.example.com/slow", Status: 200, ContentType: "text/plain", DelayMs: 1500,
			Body: "finally!\n"},
		{URL: "https://example.com/redirect", Status: 302, Location: "http://169.254.169.254/latest/meta-data/", DelayMs: 40},
		{URL: "http://169.254.169.254/latest/meta-data/", Status: 200, ContentType: "text/plain", DelayMs: 5,
			Body: "iam/security-credentials/admin-role\n"},
	}
}

// statusText avoids importing net/http here (it would add megabytes to the
// wasm build, which never uses real HTTP).
var statusText = map[int]string{200: "OK", 201: "Created", 204: "No Content", 301: "Moved Permanently",
	302: "Found", 304: "Not Modified", 400: "Bad Request", 401: "Unauthorized", 403: "Forbidden",
	404: "Not Found", 500: "Internal Server Error", 502: "Bad Gateway", 503: "Service Unavailable"}
