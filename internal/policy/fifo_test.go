package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mkfifo makes a FIFO nobody writes to or reads from.
func mkfifo(t *testing.T, path string) {
	t.Helper()
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

// A *.cedar that is a FIFO, or a link to one, is a load error, as a file
// that cannot be read is: policy_dir of a project needs no trust, and the
// policies are loaded on every request.
func TestLoadFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(permitAll), 0o600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "x.cedar")
	mkfifo(t, fifo)
	linked := t.TempDir()
	link := filepath.Join(linked, "y.cedar")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, file string }{{dir, fifo}, {linked, link}} {
		err := quick(t, fifo, func() error {
			_, err := Load(context.Background(), c.dir, Rules{})
			return err
		})
		if err == nil || !strings.Contains(err.Error(), c.file+": not a regular file") {
			t.Errorf("%s: %v", c.file, err)
		}
		var cache Cache
		err = quick(t, fifo, func() error {
			_, err := cache.Engine(context.Background(), c.dir, Rules{})
			return err
		})
		if err == nil {
			t.Errorf("%s: the cache gave an engine", c.file)
		}
	}
}
