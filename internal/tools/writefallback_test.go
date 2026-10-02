package tools

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWriteHardLink(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(a, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if readBack(t, a) != "new" || readBack(t, b) != "new" {
		t.Errorf("a %q, b %q; want both new", readBack(t, a), readBack(t, b))
	}
	if !os.SameFile(must(os.Stat(a)), must(os.Stat(b))) {
		t.Error("the names no longer share a file")
	}
	noTemp(t, dir)
}

func isFIFO(t *testing.T, path string) bool {
	t.Helper()
	return must(os.Lstat(path)).Mode()&os.ModeNamedPipe != 0
}

func TestWriteFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "p")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Opened for reading and writing so that the reader is there before
	// the write opens the FIFO, and the read waits for the data rather
	// than taking an empty FIFO for its end.
	r, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// More than the pipe holds: the write waits for the reader to drain it.
	want := strings.Repeat("through the pipe\n", 10000)
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, len(want))
		n, _ := io.ReadFull(r, buf)
		got <- string(buf[:n])
	}()
	if err := writeAtomic(fifo, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != want {
			t.Errorf("reader got %d bytes, want %d", len(s), len(want))
		}
	case <-time.After(5 * time.Second):
		t.Error("the write did not reach the reader")
	}
	if !isFIFO(t, fifo) {
		t.Error("the FIFO was replaced by a file")
	}
	noTemp(t, dir)
}

func TestWriteFIFONoReader(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "p")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- writeAtomic(fifo, []byte("x"), 0o600) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("wrote to a FIFO nobody reads")
		}
	case <-time.After(5 * time.Second):
		// Free the writer stuck in open(2).
		if r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			defer r.Close()
		}
		t.Fatal("the write waits for a reader")
	}
	if !isFIFO(t, fifo) {
		t.Error("the FIFO was replaced by a file")
	}
	noTemp(t, dir)
}

func TestWriteDevice(t *testing.T) {
	const dev = "/dev/null"
	if st, err := os.Stat(dev); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		t.Skip("no /dev/null")
	}
	if err := writeAtomic(dev, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if must(os.Stat(dev)).Mode()&os.ModeCharDevice == 0 {
		t.Error("the device was replaced")
	}
}

func TestWriteClosedDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root creates files in a read-only directory")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if err := writeAtomic(f, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, f); got != "new" {
		t.Errorf("got %q, want new", got)
	}
	// A file the directory has no room for is still refused.
	if err := writeAtomic(filepath.Join(dir, "g"), []byte("x"), 0o644); !errors.Is(err, os.ErrPermission) {
		t.Errorf("new file in a closed directory: %v", err)
	}
}

func TestWriteForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root gives a file to another user")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	const nobody = 65534
	if err := os.Chown(f, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(f, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, f); got != "new" {
		t.Errorf("got %q, want new", got)
	}
	if st := must(os.Stat(f)).Sys().(*syscall.Stat_t); st.Uid != nobody || st.Gid != nobody {
		t.Errorf("owner %d:%d, want %d:%d", st.Uid, st.Gid, nobody, nobody)
	}
	noTemp(t, dir)
}

func TestWriteForeignGroup(t *testing.T) {
	groups, _ := os.Getgroups()
	gid := -1
	for _, g := range groups {
		if g != os.Getegid() {
			gid = g
			break
		}
	}
	if gid < 0 {
		t.Skip("no group to give a file to but the user's own")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("old"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(f, -1, gid); err != nil {
		t.Skip(err)
	}
	if err := writeAtomic(f, []byte("new"), 0o664); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, f); got != "new" {
		t.Errorf("got %q, want new", got)
	}
	if g := must(os.Stat(f)).Sys().(*syscall.Stat_t).Gid; g != uint32(gid) {
		t.Errorf("group %d, want %d", g, gid)
	}
	noTemp(t, dir)
}

func TestWriteAtomicReplaces(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := must(os.Stat(f))
	if err := writeAtomic(f, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, f); got != "new" {
		t.Errorf("got %q, want new", got)
	}
	// A new file in its place: written whole and renamed over the old one.
	if os.SameFile(before, must(os.Stat(f))) {
		t.Error("written in place, not replaced")
	}
	noTemp(t, dir)
}
