package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// layout:
//
//	root/
//	  data/a.txt
//	  data/sub/b.txt
//	  data/link_out -> root/secret   (symlink escaping data/)
//	  data/link_in  -> root/data/sub (symlink staying inside)
//	  data2/c.txt                    (prefix-confusion trap: "data" vs "data2")
//	  secret/key.txt
func setupTree(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"data/sub", "data2", "secret"} {
		must(os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	for _, f := range []string{"data/a.txt", "data/sub/b.txt", "data2/c.txt", "secret/key.txt"} {
		must(os.WriteFile(filepath.Join(root, f), []byte(f), 0o644))
	}
	must(os.Symlink(filepath.Join(root, "secret"), filepath.Join(root, "data/link_out")))
	must(os.Symlink(filepath.Join(root, "data/sub"), filepath.Join(root, "data/link_in")))
	return root
}

func TestCheckReadPaths(t *testing.T) {
	root := setupTree(t)
	g, err := NewPathGrant(false, []string{filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	p := &Permissions{Read: g}
	j := func(s string) string { return root + "/" + s } // no Clean on purpose

	cases := []struct {
		path  string
		allow bool
	}{
		{j("data/a.txt"), true},
		{j("data/sub/b.txt"), true},
		{j("data/sub/../a.txt"), true},              // ".." that stays inside
		{j("data"), true},                           // the granted dir itself
		{j("data/link_in/b.txt"), true},             // symlink that stays inside
		{j("data/../secret/key.txt"), false},        // classic ../ escape
		{j("data/sub/../../secret/key.txt"), false}, // deeper ../ escape
		{j("data/link_out/key.txt"), false},         // symlink escape
		{j("data/link_out"), false},                 // the escaping link itself
		{j("data2/c.txt"), false},                   // prefix confusion
		{j("data/link_in/../a.txt"), true},          // link_in -> data/sub, so link_in/.. is data/
		{"/etc/passwd", false},
		{j("data/missing.txt"), true}, // non-existent but inside (resolves fine)
		{j("data/nope/../../secret/key.txt"), false},
	}
	for _, c := range cases {
		_, err := p.CheckRead(c.path)
		got := err == nil
		if got != c.allow {
			t.Errorf("CheckRead(%q) allowed=%v, want %v (err=%v)", c.path, got, c.allow, err)
		}
		if err != nil {
			var pe *PermissionError
			if !errors.As(err, &pe) {
				t.Errorf("CheckRead(%q): error is %T, want *PermissionError", c.path, err)
			}
		}
	}
}

func TestDotDotAfterSymlinkIsKernelSemantics(t *testing.T) {
	// data/link_out -> secret. Lexically "data/link_out/../a.txt" is
	// "data/a.txt" (allowed), but the kernel resolves link_out first and
	// opens "root/a.txt" — outside data/. Our resolver must agree with the
	// kernel, so the check must fail.
	root := setupTree(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, _ := NewPathGrant(false, []string{filepath.Join(root, "data")})
	p := &Permissions{Read: g}
	got, err := p.CheckRead(root + "/data/link_out/../a.txt")
	if err == nil {
		t.Fatalf("expected denial, got allowed path %q", got)
	}
}

func TestCheckWriteNewFile(t *testing.T) {
	root := setupTree(t)
	g, _ := NewPathGrant(false, []string{filepath.Join(root, "data")})
	p := &Permissions{Write: g}
	real, err := p.CheckWrite(root + "/data/new/deeper.txt")
	if err != nil {
		t.Fatalf("new file in granted dir should be allowed: %v", err)
	}
	canonRoot, _ := filepath.EvalSymlinks(root)
	if want := filepath.Join(canonRoot, "data/new/deeper.txt"); real != want {
		t.Errorf("canonical path = %q, want %q", real, want)
	}
	if _, err := p.CheckWrite(root + "/data/link_out/new.txt"); err == nil {
		t.Error("write through escaping symlink should be denied")
	}
	if _, err := p.CheckWrite(root + "/elsewhere.txt"); err == nil {
		t.Error("write outside grant should be denied")
	}
}

func TestDenyByDefault(t *testing.T) {
	var p Permissions // zero value
	if _, err := p.CheckRead("go.mod"); err == nil {
		t.Error("read should be denied by default")
	}
	if _, err := p.CheckWrite("x"); err == nil {
		t.Error("write should be denied by default")
	}
	if err := p.CheckNet("example.com"); err == nil {
		t.Error("net should be denied by default")
	}
	if err := p.CheckEnv("HOME"); err == nil {
		t.Error("env should be denied by default")
	}
}

func TestGrantMustExist(t *testing.T) {
	if _, err := NewPathGrant(false, []string{"/definitely/not/here"}); err == nil {
		t.Error("granting a non-existent path should fail")
	}
}

func TestCheckNet(t *testing.T) {
	p := &Permissions{Net: Grant{Items: []string{"example.com", "api.local:8080"}}}
	cases := map[string]bool{
		"example.com":      true,
		"EXAMPLE.com:443":  true, // host-only grant allows any port
		"example.com.":     true,
		"evil.example.com": false, // no implicit subdomains
		"example.com.evil": false,
		"api.local:8080":   true,
		"api.local:9090":   false,
		"api.local":        false,
		"127.0.0.1":        false,
	}
	for host, want := range cases {
		if got := p.CheckNet(host) == nil; got != want {
			t.Errorf("CheckNet(%q)=%v, want %v", host, got, want)
		}
	}
}

func TestCheckEnvList(t *testing.T) {
	p := &Permissions{Env: Grant{Items: []string{"HOME"}}}
	if p.CheckEnv("HOME") != nil || p.CheckEnv("PATH") == nil {
		t.Error("env allowlist not respected")
	}
}
