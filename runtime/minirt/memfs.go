package minirt

// memfs.go — an in-memory filesystem with directories, files and symlinks.
//
// Used by the browser build (there is no disk in a web page) and by tests.
// EvalSymlinks implements the same resolution order as the kernel: walk the
// path one component at a time; when a component is a symlink, splice its
// target in front of the remaining components and continue from the link's
// parent (or from "/" for an absolute target); ".." pops the *resolved*
// directory. So "link/.." is the parent of the link's target, not the
// directory containing the link — exactly the trap permissions.go guards
// against.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type memNode struct {
	dir     bool
	link    string // symlink target ("" if not a link)
	data    string
	modTime time.Time
}

// MemFS is safe for concurrent use (ops may run on worker goroutines).
type MemFS struct {
	mu      sync.RWMutex
	nodes   map[string]*memNode // canonical absolute path -> node; "/" always exists
	cwd     string
	latency time.Duration
	clock   func() time.Time
}

// NewMemFS returns an empty filesystem whose working directory is cwd
// (created if needed). latency is the simulated duration of each async op.
func NewMemFS(cwd string, latency time.Duration) *MemFS {
	m := &MemFS{nodes: map[string]*memNode{"/": {dir: true}}, cwd: "/", latency: latency, clock: time.Now}
	_ = m.MkdirAll(cwd)
	m.cwd = path.Clean("/" + cwd)
	return m
}

// SetClock sets the source of modification times (the loop's virtual clock).
func (m *MemFS) SetClock(now func() time.Time) { m.mu.Lock(); m.clock = now; m.mu.Unlock() }

func (m *MemFS) Getwd() (string, error) { return m.cwd, nil }
func (m *MemFS) Latency() time.Duration { return m.latency }

// MkdirAll creates a directory and its parents (setup helper; no symlinks).
func (m *MemFS) MkdirAll(p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = path.Clean("/" + p)
	cur := "/"
	for _, c := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if c == "" {
			continue
		}
		cur = path.Join(cur, c)
		if n, ok := m.nodes[cur]; ok {
			if !n.dir {
				return fmt.Errorf("mkdir %s: not a directory", cur)
			}
			continue
		}
		m.nodes[cur] = &memNode{dir: true}
	}
	return nil
}

// AddFile creates a file (and parent directories). Setup helper.
func (m *MemFS) AddFile(p, data string) error {
	p = path.Clean("/" + p)
	if err := m.MkdirAll(path.Dir(p)); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[p] = &memNode{data: data, modTime: m.clock()}
	return nil
}

// AddSymlink creates p -> target. Setup helper.
func (m *MemFS) AddSymlink(p, target string) error {
	p = path.Clean("/" + p)
	if err := m.MkdirAll(path.Dir(p)); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[p] = &memNode{link: target}
	return nil
}

func notExist(p string) error { return &fs.PathError{Op: "open", Path: p, Err: os.ErrNotExist} }

// EvalSymlinks resolves an absolute path with kernel semantics. Every
// component must exist.
func (m *MemFS) EvalSymlinks(p string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("EvalSymlinks: %q is not absolute", p)
	}
	rest := strings.Split(p, "/")
	resolved := []string{} // components of the resolved directory
	links := 0
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		cand := "/" + strings.Join(append(append([]string{}, resolved...), c), "/")
		n, ok := m.nodes[cand]
		if !ok {
			return "", notExist(cand)
		}
		if n.link != "" {
			links++
			if links > 40 {
				return "", fmt.Errorf("%s: too many levels of symbolic links", p)
			}
			target := strings.Split(n.link, "/")
			if strings.HasPrefix(n.link, "/") {
				resolved = resolved[:0]
			}
			rest = append(target, rest...)
			continue
		}
		if !n.dir && strings.Join(rest, "") != "" {
			// a file used as a directory
			return "", &fs.PathError{Op: "open", Path: cand, Err: errors.New("not a directory")}
		}
		resolved = append(resolved, c)
	}
	return "/" + strings.Join(resolved, "/"), nil
}

func (m *MemFS) ReadFile(p string, limit int64) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[p]
	switch {
	case !ok:
		return "", notExist(p)
	case n.link != "":
		return "", &fs.PathError{Op: "open", Path: p, Err: errors.New("too many levels of symbolic links (O_NOFOLLOW)")}
	case n.dir:
		return "", &fs.PathError{Op: "read", Path: p, Err: errors.New("is a directory")}
	case int64(len(n.data)) > limit:
		return "", fmt.Errorf("%s: file larger than %d bytes", p, limit)
	}
	return n.data, nil
}

func (m *MemFS) WriteFile(p, data string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, ok := m.nodes[path.Dir(p)]
	if !ok || !parent.dir {
		return notExist(path.Dir(p))
	}
	if n, ok := m.nodes[p]; ok {
		if n.link != "" {
			return &fs.PathError{Op: "open", Path: p, Err: errors.New("too many levels of symbolic links (O_NOFOLLOW)")}
		}
		if n.dir {
			return &fs.PathError{Op: "open", Path: p, Err: errors.New("is a directory")}
		}
	}
	m.nodes[p] = &memNode{data: data, modTime: m.clock()}
	return nil
}

// MemEntry describes one node for display.
type MemEntry struct {
	Path    string `json:"path"`
	Type    string `json:"type"` // "dir", "file", "symlink"
	Target  string `json:"target,omitempty"`
	Size    int    `json:"size,omitempty"`
	Content string `json:"content,omitempty"`
	ModTime int64  `json:"mtime,omitempty"` // unix ms
}

// List returns every node sorted by path. Contents of files up to maxContent
// bytes are included.
func (m *MemFS) List(maxContent int) []MemEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]MemEntry, 0, len(m.nodes))
	for p, n := range m.nodes {
		e := MemEntry{Path: p}
		switch {
		case n.link != "":
			e.Type, e.Target = "symlink", n.link
		case n.dir:
			e.Type = "dir"
		default:
			e.Type, e.Size = "file", len(n.data)
			if len(n.data) <= maxContent {
				e.Content = n.data
			}
			if !n.modTime.IsZero() {
				e.ModTime = n.modTime.UnixMilli()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
