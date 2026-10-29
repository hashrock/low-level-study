package minirt

import (
	"errors"
	"os"
	"testing"
)

// Same layout and expectations as permissions_test.go's setupTree, but on
// MemFS: the in-memory resolver must agree with the kernel.
func memTree(t *testing.T) *MemFS {
	t.Helper()
	m := NewMemFS("/root", 0)
	for _, f := range []string{"/root/data/a.txt", "/root/data/sub/b.txt", "/root/data2/c.txt", "/root/secret/key.txt", "/root/a.txt"} {
		if err := m.AddFile(f, f); err != nil {
			t.Fatal(err)
		}
	}
	_ = m.AddSymlink("/root/data/link_out", "/root/secret")
	_ = m.AddSymlink("/root/data/link_in", "sub") // relative target
	_ = m.AddSymlink("/root/loop", "/root/loop")
	return m
}

func TestMemFSCheckRead(t *testing.T) {
	m := memTree(t)
	g, err := NewPathGrantFS(m, false, []string{"data"}) // relative to cwd /root
	if err != nil {
		t.Fatal(err)
	}
	p := &Permissions{Read: g, FS: m}
	cases := []struct {
		path  string
		allow bool
		canon string
	}{
		{"data/a.txt", true, "/root/data/a.txt"},
		{"/root/data/sub/../a.txt", true, "/root/data/a.txt"},
		{"data/link_in/b.txt", true, "/root/data/sub/b.txt"},
		{"data/link_in/../a.txt", true, "/root/data/a.txt"},
		{"data/../secret/key.txt", false, "/root/secret/key.txt"},
		{"data/link_out/key.txt", false, "/root/secret/key.txt"},
		{"data/link_out/../a.txt", false, "/root/a.txt"}, // kernel semantics, not lexical
		{"data2/c.txt", false, "/root/data2/c.txt"},
		{"data/new.txt", true, "/root/data/new.txt"},
		{"data/nope/../../secret/key.txt", false, ""},
		{"/root/data/../../../../root/secret/key.txt", false, "/root/secret/key.txt"},
	}
	for _, c := range cases {
		got, err := p.CheckRead(c.path)
		if (err == nil) != c.allow {
			t.Errorf("CheckRead(%q) allowed=%v want %v (%v)", c.path, err == nil, c.allow, err)
			continue
		}
		if c.allow && got != c.canon {
			t.Errorf("CheckRead(%q) = %q, want %q", c.path, got, c.canon)
		}
		var pe *PermissionError
		if !c.allow && errors.As(err, &pe) && pe.Resolved != c.canon {
			t.Errorf("CheckRead(%q) resolved %q, want %q", c.path, pe.Resolved, c.canon)
		}
	}
	if _, err := m.EvalSymlinks("/root/loop/x"); err == nil {
		t.Error("symlink loop should fail")
	}
	if _, err := m.EvalSymlinks("/root/a.txt/x"); err == nil {
		t.Error("file used as directory should fail")
	}
}

func TestMemFSReadWrite(t *testing.T) {
	m := memTree(t)
	if s, err := m.ReadFile("/root/data/a.txt", 100); err != nil || s != "/root/data/a.txt" {
		t.Errorf("read: %q %v", s, err)
	}
	if _, err := m.ReadFile("/root/data/link_out", 100); err == nil {
		t.Error("reading a symlink directly must fail (O_NOFOLLOW)")
	}
	if _, err := m.ReadFile("/root/none", 100); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file error = %v", err)
	}
	if err := m.WriteFile("/root/data/new.txt", "x"); err != nil {
		t.Error(err)
	}
	if err := m.WriteFile("/root/nodir/new.txt", "x"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("write into missing dir = %v", err)
	}
	if err := m.WriteFile("/root/data/link_out", "x"); err == nil {
		t.Error("writing through a final symlink must fail")
	}
	if _, err := m.ReadFile("/root/data/a.txt", 3); err == nil {
		t.Error("limit not enforced")
	}
}
