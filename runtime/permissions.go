package main

// permissions.go — deny-by-default capability checks.
//
// The JS engine (goja) has no I/O of its own: a script can only touch the
// outside world through native functions that *we* install. Every such
// function calls one of the Check* methods below before doing anything.
// If no grant matches, it returns a *PermissionError which the op layer turns
// into a JS exception (Sandbox.errors.PermissionDenied).

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// PermissionError is returned when a capability was not granted.
type PermissionError struct {
	Kind   string // "read", "write", "net", "env"
	Target string // what was requested
	Flag   string // hint: the flag that would grant it
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("Requires %s access to %q, run again with the %s flag", e.Kind, e.Target, e.Flag)
}

// Grant is one capability class. All=true means "--allow-x" with no list.
type Grant struct {
	All   bool
	Items []string
}

func (g Grant) Granted() bool { return g.All || len(g.Items) > 0 }

// Permissions holds what the user granted on the command line.
// The zero value denies everything.
type Permissions struct {
	Read  Grant // Items: canonical (symlink-resolved, absolute) directory or file paths
	Write Grant
	Net   Grant // Items: "host" or "host:port"
	Env   Grant // Items: variable names
}

// NewPathGrant canonicalizes the given paths at startup. A granted path that
// does not exist is an error: we refuse to guess what it will become later.
func NewPathGrant(all bool, paths []string) (Grant, error) {
	g := Grant{All: all}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return g, err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return g, fmt.Errorf("cannot grant %q: %w", p, err)
		}
		g.Items = append(g.Items, real)
	}
	return g, nil
}

// ResolvePath turns a user supplied path into a canonical absolute path with
// every symlink and ".." resolved *the way the kernel would* (".." is applied
// after following a symlink, not lexically).
//
// For a path whose final components do not exist yet (writing a new file),
// we resolve the longest existing prefix and append the missing names. A
// missing component followed by ".." is rejected because there is no
// filesystem object to anchor the "..": the kernel would fail with ENOENT
// anyway, and lexically collapsing it could only make checks *looser*.
func ResolvePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if strings.IndexByte(p, 0) >= 0 {
		return "", errors.New("path contains NUL byte")
	}
	if !filepath.IsAbs(p) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Deliberately not filepath.Join: Join calls Clean, which would
		// collapse "link/.." lexically before symlinks are looked at.
		p = cwd + string(filepath.Separator) + p
	}
	return resolve(p, 0)
}

func resolve(p string, depth int) (string, error) {
	if depth > 255 {
		return "", errors.New("path too deep")
	}
	real, err := filepath.EvalSymlinks(p)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Strip trailing separators, then split off the last component.
	trimmed := strings.TrimRight(p, string(filepath.Separator))
	i := strings.LastIndexByte(trimmed, filepath.Separator)
	if i < 0 {
		return "", err
	}
	parent, base := trimmed[:i], trimmed[i+1:]
	if parent == "" {
		parent = string(filepath.Separator)
	}
	if base == ".." || base == "." {
		return "", fmt.Errorf("cannot resolve %q: %q after a non-existent component", p, base)
	}
	rp, err := resolve(parent, depth+1)
	if err != nil {
		return "", err
	}
	return filepath.Join(rp, base), nil
}

// within reports whether canonical path p is equal to or below dir.
func within(p, dir string) bool {
	if p == dir {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(p, dir)
}

func (g Grant) allowsPath(canonical string) bool {
	if g.All {
		return true
	}
	for _, d := range g.Items {
		if within(canonical, d) {
			return true
		}
	}
	return false
}

// CheckRead returns the canonical path to actually open. Callers must use the
// returned path, not the original string, so the check and the open agree.
func (p *Permissions) CheckRead(path string) (string, error) {
	return p.checkPath(p.Read, "read", "--allow-read", path)
}

func (p *Permissions) CheckWrite(path string) (string, error) {
	return p.checkPath(p.Write, "write", "--allow-write", path)
}

func (p *Permissions) checkPath(g Grant, kind, flag, path string) (string, error) {
	// Fail fast without touching the filesystem when nothing is granted:
	// even resolving a path leaks existence information via error messages.
	if !g.Granted() {
		return "", &PermissionError{kind, path, flag}
	}
	canonical, err := ResolvePath(path)
	if err != nil {
		return "", &PermissionError{kind, path, flag}
	}
	if !g.allowsPath(canonical) {
		return "", &PermissionError{kind, path, flag}
	}
	return canonical, nil
}

// CheckNet checks a URL host ("example.com" or "example.com:8443").
// A grant without a port allows any port on that host.
func (p *Permissions) CheckNet(hostport string) error {
	if p.Net.All {
		return nil
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, item := range p.Net.Items {
		ih, ip, err := net.SplitHostPort(item)
		if err != nil {
			ih, ip = item, ""
		}
		ih = strings.ToLower(strings.TrimSuffix(ih, "."))
		if ih == host && (ip == "" || ip == port) {
			return nil
		}
	}
	return &PermissionError{"net", hostport, "--allow-net"}
}

func (p *Permissions) CheckEnv(name string) error {
	if p.Env.All {
		return nil
	}
	for _, n := range p.Env.Items {
		if n == name {
			return nil
		}
	}
	return &PermissionError{"env", name, "--allow-env"}
}

// Query is the side-effect free variant exposed as Sandbox.permissions.query.
func (p *Permissions) Query(kind, target string) bool {
	var err error
	switch kind {
	case "read":
		_, err = p.CheckRead(target)
	case "write":
		_, err = p.CheckWrite(target)
	case "net":
		err = p.CheckNet(target)
	case "env":
		err = p.CheckEnv(target)
	default:
		return false
	}
	return err == nil
}
