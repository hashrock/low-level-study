//go:build !js

package minirt

// net_http.go — the real network (not built for js/wasm: net/http would add
// megabytes to the browser build, which uses FakeNetwork).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func defaultNetwork() Network { return HTTPNetwork{} }

// HTTPNetwork uses net/http.
type HTTPNetwork struct {
	Timeout time.Duration
}

func (n HTTPNetwork) Fetch(ctx context.Context, fr FetchRequest, checkHop func(string) error) (*FetchResponse, error) {
	timeout := n.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		// Runs on the worker goroutine; checkHop only reads immutable
		// permission data (and the goroutine-safe tracer).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return checkHop(req.URL.Host)
		},
	}
	req, err := http.NewRequestWithContext(ctx, fr.Method, fr.URL, strings.NewReader(fr.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range fr.Headers {
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
	return &FetchResponse{Status: resp.StatusCode, StatusText: resp.Status,
		URL: resp.Request.URL.String(), Headers: h, Body: string(data)}, nil
}
