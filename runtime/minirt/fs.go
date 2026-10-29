package minirt

// fs.go — the filesystem abstraction.
//
// Every file access a script can make goes through an FS. The CLI uses OSFS
// (the real filesystem); the browser build uses MemFS (memfs.go), an
// in-memory tree with directories, files and symlinks, so that the same
// permission logic (kernel-style ".." and symlink resolution) can be shown
// without a real disk.
//
// The permission layer only needs two primitives from an FS:
//   - Getwd, to make relative paths absolute;
//   - EvalSymlinks, which must resolve an *absolute* path the way the kernel
//     would (symlinks first, ".." applied to the resolved directory) and
//     fail with an error wrapping os.ErrNotExist if a component is missing.
// ResolvePathFS (permissions.go) builds the "non-existent tail" logic on top.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FS is what ops and permission checks use to touch files.
type FS interface {
	Getwd() (string, error)
	EvalSymlinks(abs string) (string, error)
	// ReadFile opens a canonical path without following a final symlink.
	ReadFile(canonical string, limit int64) (string, error)
	// WriteFile creates/truncates a canonical path without following a final symlink.
	WriteFile(canonical, data string) error
	// Latency is the simulated time one async op takes (0 for real I/O,
	// where the time is whatever the syscall takes).
	Latency() time.Duration
}

// OSFS is the real filesystem.
type OSFS struct{}

func (OSFS) Getwd() (string, error)                  { return os.Getwd() }
func (OSFS) EvalSymlinks(abs string) (string, error) { return filepath.EvalSymlinks(abs) }
func (OSFS) Latency() time.Duration                  { return 0 }

func (OSFS) ReadFile(path string, limit int64) (string, error) {
	// O_NOFOLLOW: path is already canonical, so if the final component is a
	// symlink now, someone swapped it in after our check. Refuse.
	f, err := os.OpenFile(path, os.O_RDONLY|oNOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("%s: file larger than %d bytes", path, limit)
	}
	return string(data), nil
}

func (OSFS) WriteFile(path, data string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|oNOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
