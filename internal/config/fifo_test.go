package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// mkfifo makes a FIFO nobody writes to or reads from.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip(err)
	}
}

// quick runs f and fails the test if it takes a second: a FIFO opened for
// reading waits in open(2) for a writer. The FIFO is then opened for
// reading and writing, which lets the reader stuck in open(2) go and see
// the end of the FIFO, so that the test fails rather than hangs.
func quick[T any](t *testing.T, fifo string, f func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v
	case <-time.After(time.Second):
		if w, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			w.Close()
		}
		t.Fatal("waits for a writer of the FIFO")
	}
	panic("unreachable")
}

type found struct {
	cfg  Config
	file string
	err  error
}

// A FIFO in place of the project file, or a link to one, is as if there
// were none: the search goes on up, to the file of the repository's root.
func TestProjectFIFO(t *testing.T) {
	root := trustHome(t)
	top := filepath.Join(root, "srv")
	repo(t, top, "max_steps = 7\n")
	app := filepath.Join(top, "app")
	fifo := filepath.Join(app, ProjectFile)
	mkfifo(t, fifo)
	lib := filepath.Join(top, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, filepath.Join(lib, ProjectFile)); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{app, lib} {
		got := quick(t, fifo, func() found {
			cfg, file, err := Project(Default(), cwd)
			return found{cfg, file, err}
		})
		if want := filepath.Join(top, ProjectFile); got.err != nil || got.file != want || got.cfg.MaxSteps != 7 {
			t.Errorf("%s: %q, max_steps %d, %v; want %s", cwd, got.file, got.cfg.MaxSteps, got.err, want)
		}
	}
	// With nothing above, there is no project.
	alone := filepath.Join(root, "alone")
	mkfifo(t, filepath.Join(alone, ProjectFile))
	if err := os.MkdirAll(filepath.Join(alone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := quick(t, filepath.Join(alone, ProjectFile), func() found {
		cfg, file, err := Project(Default(), alone)
		return found{cfg, file, err}
	})
	if got.err != nil || got.file != "" || got.cfg.MaxSteps != Default().MaxSteps {
		t.Errorf("a FIFO alone: %q, max_steps %d, %v", got.file, got.cfg.MaxSteps, got.err)
	}
	// A link to a regular file is taken as it was.
	linked := filepath.Join(root, "linked")
	if err := os.MkdirAll(filepath.Join(linked, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(top, ProjectFile), filepath.Join(linked, ProjectFile)); err != nil {
		t.Fatal(err)
	}
	if cfg, file, err := Project(Default(), linked); err != nil || file != filepath.Join(linked, ProjectFile) || cfg.MaxSteps != 7 {
		t.Errorf("a link to a file: %q, max_steps %d, %v", file, cfg.MaxSteps, err)
	}
}
