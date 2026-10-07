package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/session"
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

// A mentioned FIFO, or a link to one, or a device is not attached: the
// read would wait for a writer (a FIFO, /dev/tty) or never end (/dev/zero),
// and the proxy with it.
func TestMentionNotRegular(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	mkfifo(t, fifo)
	if err := os.Symlink("pipe", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	notes := func(text string) []string {
		var out []string
		for _, e := range quick(t, fifo, func() []session.Entry { return mentions(text, dir) }) {
			out = append(out, mentionNote(e, dir))
		}
		return out
	}
	want := []string{"@pipe: not a regular file, not attached", "@link: not a regular file, not attached"}
	if got := notes("@pipe @link"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", got, want)
	}
	// A device: /dev/null first, which ends, so that a read of devices
	// fails here rather than with /dev/zero eating the memory.
	for _, dev := range []string{"/dev/null", "/dev/zero"} {
		if st, err := os.Stat(dev); err != nil || st.Mode()&os.ModeCharDevice == 0 {
			t.Skipf("%s: %v", dev, err)
		}
		if got := notes("@" + dev); len(got) != 1 || got[0] != "@"+dev+": not a regular file, not attached" {
			t.Fatalf("@%s: %q", dev, got)
		}
	}
}

// A file is read up to what can be shown of it, not whole into the proxy.
func TestMentionReadsLimit(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("0123456789\n", 10000)), 0o644); err != nil {
		t.Fatal(err)
	}
	// What follows the lines is a hole, which costs the disk nothing.
	if err := os.Truncate(big, 64<<20); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	es := mentions("@big.txt", dir)
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
		t.Errorf("%d MB allocated for a mention", n>>20)
	}
	if len(es) != 1 || es[0].IsError || !strings.Contains(es[0].Text, "\n[truncated; continue with read_file offset=") {
		t.Errorf("%+v", es)
	}
}

// A line the limit of the read cuts is shown if read_file would show it
// cut all the same, and the rest is left to read_file.
func TestMentionLongLines(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		text  string
		lines int // shown
		next  int // the offset to continue with
	}{
		// One line longer than the read: its start is what read_file shows.
		{"one", strings.Repeat("a", 100000), 1, 2},
		// The second line is cut short of what read_file would show.
		{"short", strings.Repeat("a", 39000) + "\n" + strings.Repeat("b", 3000) + "\n", 1, 2},
		// The second line is cut past what read_file would show.
		{"long", strings.Repeat("a", 30000) + "\n" + strings.Repeat("b", 20000) + "\nc\n", 2, 3},
	}
	for _, c := range cases {
		if err := os.WriteFile(filepath.Join(dir, c.name), []byte(c.text), 0o644); err != nil {
			t.Fatal(err)
		}
		es := mentions("@"+c.name, dir)
		if len(es) != 1 || es[0].IsError {
			t.Fatalf("%s: %+v", c.name, es)
		}
		lines := strings.Split(strings.TrimSuffix(es[0].Text, "\n"), "\n")
		if len(lines) != c.lines+1 {
			t.Errorf("%s: %d lines, want %d and the mark: %.200q", c.name, len(lines), c.lines+1, es[0].Text)
			continue
		}
		for i, l := range lines[:c.lines] {
			if !strings.HasSuffix(l, "[...]") || len(l) != 7+2000+5 {
				t.Errorf("%s: line %d is %d bytes: %.40q", c.name, i+1, len(l), l)
			}
		}
		if mark := lines[c.lines]; mark != "[truncated; continue with read_file offset="+strconv.Itoa(c.next)+"]" {
			t.Errorf("%s: %.80q", c.name, mark)
		}
	}
}
