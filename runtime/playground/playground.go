// Package playground wires minirt to a simulated world for the browser
// build: an in-memory filesystem preloaded with the repo's examples, a fake
// network with canned responses, a fake environment, a virtual clock and
// the event-loop trace. It has no syscall/js dependency so it can be tested
// with plain `go test`; cmd/wasm is a thin wrapper around it.
package playground

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"minirt/examples"
	"minirt/minirt"
)

// Cwd is the working directory inside the virtual filesystem. The examples
// live under Cwd/examples, so relative paths in the example scripts
// ("examples/data/input.txt") and the README's flags work unchanged.
const Cwd = "/app"

// Grant mirrors one --allow-* flag: On=false means the flag is absent;
// On=true with an empty Value means "--allow-x" (everything).
type Grant struct {
	On    bool   `json:"on"`
	Value string `json:"value"`
}

func (g Grant) list() []string {
	var out []string
	for _, s := range strings.Split(g.Value, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (g Grant) all() bool { return g.On && len(g.list()) == 0 }

type Config struct {
	Name        string             `json:"name"` // file name for the script, e.g. "timers.js"
	Source      string             `json:"source"`
	AllowRead   Grant              `json:"allowRead"`
	AllowWrite  Grant              `json:"allowWrite"`
	AllowNet    Grant              `json:"allowNet"`
	AllowEnv    Grant              `json:"allowEnv"`
	TimeoutMs   float64            `json:"timeoutMs"`
	FSLatencyMs float64            `json:"fsLatencyMs"`
	Routes      []minirt.FakeRoute `json:"routes"`
	Args        []string           `json:"args"`
	Env         map[string]string  `json:"env"`
}

// Flags renders the config as the equivalent CLI command line.
func (c Config) Flags() string {
	parts := []string{"minirt", "examples/" + c.scriptName()}
	add := func(flag string, g Grant) {
		if !g.On {
			return
		}
		if g.all() {
			parts = append(parts, "--"+flag)
		} else {
			parts = append(parts, "--"+flag+"="+strings.Join(g.list(), ","))
		}
	}
	add("allow-read", c.AllowRead)
	add("allow-write", c.AllowWrite)
	add("allow-net", c.AllowNet)
	add("allow-env", c.AllowEnv)
	if c.TimeoutMs > 0 {
		parts = append(parts, fmt.Sprintf("--timeout=%gms", c.TimeoutMs))
	}
	return strings.Join(parts, " ")
}

func (c Config) scriptName() string {
	n := path.Base(strings.TrimSpace(c.Name))
	if n == "" || n == "." || n == "/" {
		n = "editor.js"
	}
	return n
}

// Examples returns the embedded example scripts by file name.
func Examples() map[string]string {
	out := map[string]string{}
	entries, _ := fs.ReadDir(examples.FS, ".")
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			b, _ := fs.ReadFile(examples.FS, e.Name())
			out[e.Name()] = string(b)
		}
	}
	return out
}

// DefaultEnv is the fake environment.
func DefaultEnv() map[string]string {
	return map[string]string{
		"HOME": "/home/guest", "USER": "guest", "LANG": "ja_JP.UTF-8",
		"SECRET_TOKEN": "fake-token-do-not-leak",
	}
}

// NewFS builds the virtual filesystem.
func NewFS(latency time.Duration) *minirt.MemFS {
	m := minirt.NewMemFS(Cwd, latency)
	_ = fs.WalkDir(examples.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".js") || strings.HasPrefix(p, "data/")) {
			return err
		}
		b, _ := fs.ReadFile(examples.FS, p)
		return m.AddFile(path.Join(Cwd, "examples", p), string(b))
	})
	// Same trap as the repo: a symlink inside the granted dir pointing out.
	_ = m.AddSymlink(Cwd+"/examples/data/link_to_etc", "/etc")
	// Files *outside* the usual grant that the escape examples aim at.
	_ = m.AddFile(Cwd+"/go.mod", "module minirt\n\ngo 1.27\n")
	_ = m.AddFile(Cwd+"/main.go", "package main // (virtual copy) the runtime's own source, outside --allow-read=examples/data\n")
	_ = m.AddFile("/etc/passwd", "root:x:0:0:root:/root:/bin/sh\nguest:x:1000:1000:guest:/home/guest:/bin/sh\n")
	_ = m.AddFile("/etc/hostname", "minirt-playground\n")
	_ = m.AddFile("/home/guest/.ssh/id_ed25519", "-----BEGIN OPENSSH PRIVATE KEY----- (fake) -----END OPENSSH PRIVATE KEY-----\n")
	_ = m.MkdirAll("/tmp")
	return m
}

// Session is one loaded script on a virtual clock.
type Session struct {
	cfg    Config
	rt     *minirt.Runtime
	fs     *minirt.MemFS
	stdout bytes.Buffer
	stderr bytes.Buffer
	done   bool
	err    error
}

// StepResult is returned after every step.
type StepResult struct {
	Done     bool                `json:"done"`
	Error    string              `json:"error,omitempty"`
	ExitCode int                 `json:"exitCode"`
	Snapshot minirt.LoopSnapshot `json:"snapshot"`
}

// NewSession builds the world and loads the script (nothing runs yet).
// emit receives every trace event.
func NewSession(cfg Config, emit func(minirt.Event)) (*Session, error) {
	if cfg.FSLatencyMs < 0 {
		cfg.FSLatencyMs = 0
	}
	s := &Session{cfg: cfg, fs: NewFS(time.Duration(cfg.FSLatencyMs * float64(time.Millisecond)))}
	if cfg.Routes == nil {
		cfg.Routes = minirt.DefaultFakeRoutes()
	}
	env := cfg.Env
	if env == nil {
		env = DefaultEnv()
	}
	opts := minirt.Options{
		Timeout:      time.Duration(cfg.TimeoutMs * float64(time.Millisecond)),
		MaxCallStack: 10000,
		Args:         cfg.Args,
		Stdout:       &s.stdout,
		Stderr:       &s.stderr,
		FS:           s.fs,
		Network:      minirt.NewFakeNetwork(cfg.Routes),
		LookupEnv:    func(n string) (string, bool) { v, ok := env[n]; return v, ok },
		VirtualClock: true,
		Trace:        emit,
	}
	var err error
	if opts.Perms.Read, err = minirt.NewPathGrantFS(s.fs, cfg.AllowRead.all(), cfg.AllowRead.list()); err != nil {
		return nil, fmt.Errorf("--allow-read: %w", err)
	}
	if opts.Perms.Write, err = minirt.NewPathGrantFS(s.fs, cfg.AllowWrite.all(), cfg.AllowWrite.list()); err != nil {
		return nil, fmt.Errorf("--allow-write: %w", err)
	}
	if cfg.AllowNet.On {
		opts.Perms.Net = minirt.Grant{All: cfg.AllowNet.all(), Items: cfg.AllowNet.list()}
	}
	if cfg.AllowEnv.On {
		opts.Perms.Env = minirt.Grant{All: cfg.AllowEnv.all(), Items: cfg.AllowEnv.list()}
	}
	opts.Perms.FS = s.fs
	s.rt, err = minirt.NewRuntime(opts)
	if err != nil {
		return nil, err
	}
	// The script lives next to the examples so require("./data/lib.js") works.
	name := path.Join(Cwd, "examples", cfg.scriptName())
	_ = s.fs.AddFile(name, cfg.Source)
	if err := s.rt.Load(name, cfg.Source); err != nil {
		s.done, s.err = true, err
		return s, nil
	}
	return s, nil
}

// Step runs one macrotask (+ microtasks).
func (s *Session) Step() StepResult {
	if !s.done {
		s.done, s.err = s.rt.Step()
	}
	return s.result()
}

// Run steps until the script finishes or maxSteps tasks have run.
func (s *Session) Run(maxSteps int) StepResult {
	for i := 0; i < maxSteps && !s.done; i++ {
		s.done, s.err = s.rt.Step()
	}
	return s.result()
}

func (s *Session) result() StepResult {
	r := StepResult{Done: s.done, Snapshot: s.rt.Snapshot()}
	if s.err != nil {
		r.Error = minirt.FormatError(s.err)
		r.ExitCode = minirt.ExitCode(s.err)
		r.Snapshot.Error = r.Error
	}
	return r
}

// Stdout / Stderr return everything written so far.
func (s *Session) Stdout() string { return s.stdout.String() }
func (s *Session) Stderr() string { return s.stderr.String() }

// Files lists the virtual filesystem (contents up to 4 KiB per file).
func (s *Session) Files() []minirt.MemEntry {
	es := s.fs.List(4096)
	sort.Slice(es, func(i, j int) bool { return es[i].Path < es[j].Path })
	return es
}

// MustJSON marshals v (helper for the wasm bridge).
func MustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}
