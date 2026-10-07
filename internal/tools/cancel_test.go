package tools

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// firstLine hands the first line written to it on line.
type firstLine struct {
	mu   sync.Mutex
	b    []byte
	sent bool
	line chan string
}

func (w *firstLine) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.sent {
		w.b = append(w.b, p...)
		if i := bytes.IndexByte(w.b, '\n'); i >= 0 {
			w.line <- string(w.b[:i])
			w.sent = true
		}
	}
	return len(p), nil
}

// groupGone is whether the process group pgid is gone within d: what
// SIGKILL hits dies, and is reaped, a moment after.
func groupGone(pgid int, d time.Duration) bool {
	for end := time.Now().Add(d); ; time.Sleep(10 * time.Millisecond) {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// Ctrl+C ends a tool with what it started: a child holding its output
// would hold the request until it ended. What does not end on SIGTERM is
// killed after groupGrace. Each tool prints its pid, its group's id, once
// what it starts is there.
func TestRunExternalCancel(t *testing.T) {
	cases := []struct {
		name, script string
		grace        bool          // ends only when killed
		after        time.Duration // from the line to the cancel
		path         string
	}{
		{name: "child", script: "sleep 30 &\necho $$\nwait\n"},
		{name: "grandchild", script: "p=$$\nsh -c \"echo $p; sleep 30; :\" &\nwait\n"},
		{name: "grandchild ignoring SIGTERM", script: "p=$$\nsh -c \"trap '' TERM; echo $p; sleep 30; :\" &\nwait\n", grace: true},
		{name: "tool ignoring SIGTERM", script: "trap '' TERM\nsleep 30 &\necho $$\nwait\n", grace: true},
		// Gone at once: whether the cancel comes before Run sees it gone
		// or after, the child goes.
		{name: "child left holding the output", script: "sleep 30 &\necho $$\n", grace: true},
		{name: "child left, cancelled late", script: "sleep 30 &\necho $$\n", grace: true, after: 300 * time.Millisecond},
	}
	// All written before any runs: a file open for writing when another
	// test forks cannot be run ("text file busy").
	dir := t.TempDir()
	for i := range cases {
		cases[i].path = filepath.Join(dir, strconv.Itoa(i))
		writeExec(t, cases[i].path, "#!/bin/sh\n"+cases[i].script, 0o755)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := external{name: "tool", path: tc.path}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			live := &firstLine{line: make(chan string, 1)}
			pgid := make(chan int, 1)
			var cancelled time.Time
			go func() {
				n := 0
				select {
				case l := <-live.line:
					n, _ = strconv.Atoi(l)
					time.Sleep(tc.after)
				case <-ctx.Done():
				}
				cancelled = time.Now()
				cancel()
				pgid <- n
			}()
			_, err := tool.Execute(ctx, Exec{}, nil, live)
			done := time.Now()
			id := <-pgid
			took := done.Sub(cancelled)
			if id <= 1 { // kill(-1) is every process
				t.Fatalf("no pid printed: %v", err)
			}
			t.Cleanup(func() { syscall.Kill(-id, syscall.SIGKILL) })
			if err == nil {
				t.Error("no error from an interrupted tool")
			}
			limit := groupGrace + time.Second
			if !tc.grace {
				limit = groupGrace / 2
			}
			if took > limit {
				t.Errorf("returned %v after the cancel, want within %v", took, limit)
			}
			if !groupGone(id, time.Second) {
				t.Errorf("process group %d still there", id)
			}
		})
	}
}

// A tool that exits leaving a child with its output, not interrupted, is
// done after groupGrace with what it printed; the child is left alone, a
// server it started, say.
func TestRunExternalLeftChild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool")
	writeExec(t, path, "#!/bin/sh\necho $$\nsleep 30 &\n", 0o755)
	tool := external{name: "tool", path: path}
	start := time.Now()
	out, err := tool.Execute(context.Background(), Exec{}, nil, nil)
	took := time.Since(start)
	id, _ := strconv.Atoi(strings.TrimSpace(out))
	if id <= 1 {
		t.Fatalf("output %q, %v", out, err)
	}
	t.Cleanup(func() { syscall.Kill(-id, syscall.SIGKILL) })
	if err != nil {
		t.Errorf("error %v", err)
	}
	if took > groupGrace+time.Second {
		t.Errorf("returned after %v, want about %v", took, groupGrace)
	}
	if groupGone(id, 0) {
		t.Error("the child was killed")
	}
}
