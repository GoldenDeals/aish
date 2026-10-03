package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mkfifo makes a FIFO nobody writes to or reads from.
func mkfifo(t *testing.T, path string, mode uint32) {
	t.Helper()
	if err := syscall.Mkfifo(path, mode); err != nil {
		t.Skip(err)
	}
	if err := os.Chmod(path, os.FileMode(mode)); err != nil {
		t.Fatal(err)
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

func TestReadFIFO(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "p")
	mkfifo(t, fifo, 0o600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink("p", link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, link} {
		err := quick(t, fifo, func() error {
			_, err := readFile(ctx, map[string]any{"path": path})
			return err
		})
		if err == nil || !strings.Contains(err.Error(), path+": not a regular file") {
			t.Errorf("read_file %s: %v", path, err)
		}
		err = quick(t, fifo, func() error {
			_, err := editFile(ctx, map[string]any{"path": path, "old_string": "a", "new_string": "b"})
			return err
		})
		if err == nil || !strings.Contains(err.Error(), path+": not a regular file") {
			t.Errorf("edit_file %s: %v", path, err)
		}
	}
	if !isFIFO(t, fifo) {
		t.Error("the FIFO was replaced")
	}
	noTemp(t, dir)
}

func TestReadNotRegular(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// A device is refused too: /dev/zero would never end, /dev/tty would
	// wait for the user.
	if st, err := os.Stat("/dev/null"); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		if _, err := readFile(ctx, map[string]any{"path": "/dev/null"}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("read_file /dev/null: %v", err)
		}
	}
	// A directory fails as it did.
	if _, err := readFile(ctx, map[string]any{"path": dir}); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("read_file of a directory: %v", err)
	}
	if _, err := editFile(ctx, map[string]any{"path": dir, "old_string": "a", "new_string": "b"}); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("edit_file of a directory: %v", err)
	}
	// A missing file too.
	if _, err := readFile(ctx, map[string]any{"path": filepath.Join(dir, "missing")}); !os.IsNotExist(err) {
		t.Errorf("read_file of a missing file: %v", err)
	}
	// A regular file, and a link to one, are read and edited.
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("f", link); err != nil {
		t.Fatal(err)
	}
	if got, err := readFile(ctx, map[string]any{"path": link}); err != nil || got != "     1\ta\n" {
		t.Errorf("read_file of a link: %q, %v", got, err)
	}
	if _, err := editFile(ctx, map[string]any{"path": link, "old_string": "a", "new_string": "b"}); err != nil {
		t.Errorf("edit_file of a link: %v", err)
	}
	if got := readBack(t, f); got != "b\n" {
		t.Errorf("got %q, want b", got)
	}
}

// A file the directory has no room for is named in the error as the agent
// asked for it, not as the temporary file that could not be created.
func TestWriteClosedDirNamesTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root creates files in a read-only directory")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	g := filepath.Join(dir, "g")
	_, err := writeFile(context.Background(), map[string]any{"path": g, "content": "x"})
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("new file in a closed directory: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), g+":") || strings.Contains(err.Error(), ".aish-") {
		t.Errorf("error %v, want it to name %s", err, g)
	}
}

func TestLoadSkipsFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	mkfifo(t, fifo, 0o755)
	tool := "#!/bin/sh\n# aish:desc A tool\necho hi\n"
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte(tool), 0o755); err != nil {
		t.Fatal(err)
	}
	r := quick(t, fifo, func() *Registry { return Load(dir) })
	if _, ok := r.Get("pipe"); ok {
		t.Error("a FIFO loaded as a tool")
	}
	if _, ok := r.Get("real"); !ok {
		t.Error("the tool next to the FIFO is not loaded")
	}
}
