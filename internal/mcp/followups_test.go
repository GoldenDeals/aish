package mcp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A session the server answered 404 to is still ended at it: the server
// may be another instance behind the same URL that keeps it. The client
// waits for the repeat of the call in the new session too.
func TestHTTPSessionClosed(t *testing.T) {
	h := &httpStub{}
	type del struct{ session, auth string }
	deleted := make(chan del, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted <- del{r.Header.Get("Mcp-Session-Id"), r.Header.Get("Authorization")}
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	m := NewManager(map[string]Server{"web": {URL: srv.URL, Timeout: 7, Headers: map[string]string{"Authorization": "Bearer t"}}}, t.TempDir())
	t.Cleanup(m.Close)
	ctx := context.Background()
	res := m.List(ctx, true)
	if len(res.Tools) != 1 || len(res.Errors) != 0 {
		t.Fatalf("listing: %+v", res)
	}
	if got, want := res.Tools[0].Timeout, startTimeout+2*7*time.Second; got != want {
		t.Errorf("timeout %v, want %v", got, want)
	}

	h.mu.Lock()
	h.session = "" // the server restarted
	h.mu.Unlock()
	raw, err := m.Call(ctx, "web_work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := Format(raw); out != "done in s2" {
		t.Errorf("result %q", out)
	}
	select {
	case got := <-deleted:
		if got != (del{"s1", "Bearer t"}) {
			t.Errorf("DELETE with session %q, authorization %q; want the old session with the headers", got.session, got.auth)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the old session was not ended")
	}
}

// A cancellation the server does not take in time is dropped, not the
// server: the call it is about is over, and the server may read on. A call
// stuck behind it gives the server up, as one stuck on its own line does.
func TestCancelUnread(t *testing.T) {
	dir := t.TempDir()
	m := clientStubManager(t, dir, 1)
	s := m.servers[0]
	s.mu.Lock()
	c := s.conn.(*stdio)
	s.mu.Unlock()
	pid := c.cmd.Process.Pid
	t.Cleanup(func() {
		select {
		case <-c.dead:
		default:
			syscall.Kill(-pid, syscall.SIGKILL) // stopped, it ignores SIGTERM
		}
	})
	ctx := context.Background()

	errc := make(chan error, 1)
	go func() {
		_, err := m.Call(ctx, "stub_work", map[string]any{"hang": true})
		errc <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(strings.Join(logLines(dir), "\n"), "hang "); {
		if time.Now().After(deadline) {
			t.Fatalf("the call did not reach the server; log %q", logLines(dir))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The server stops reading, its input full: the cancellation of the
	// call will not get in.
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	fillInput(t, c)
	if err := <-errc; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung call: %v", err)
	}
	time.Sleep(cancelTimeout + 500*time.Millisecond)
	if !c.alive() {
		t.Fatal("the server was given up for a cancellation it did not read")
	}
	if st := m.Status().Servers[0]; st.State != "running" {
		t.Errorf("status %+v", st)
	}

	if _, err := m.Call(ctx, "stub_work", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call to a server that does not read: %v", err)
	}
	if c.alive() {
		t.Fatal("a server that does not read is kept")
	}
	syscall.Kill(-pid, syscall.SIGKILL)
	select {
	case <-c.dead:
	case <-time.After(5 * time.Second):
		t.Fatal("the server is not stopped")
	}
	raw, err := m.Call(ctx, "stub_work", nil)
	if err != nil {
		t.Fatalf("next call: %v", err)
	}
	if out, _ := Format(raw); out != "done" {
		t.Errorf("next call: %q", out)
	}
	if got := strings.Count(strings.Join(logLines(dir), "\n"), "start"); got != 2 {
		t.Errorf("log %q, want two starts", logLines(dir))
	}
}

// fillInput fills the pipe to the server's stdin with empty lines, which
// the server skips if it ever reads them.
func fillInput(t *testing.T, c *stdio) {
	t.Helper()
	f, ok := c.in.(*os.File)
	if !ok {
		t.Fatalf("stdin is a %T", c.in)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := f.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	defer f.SetWriteDeadline(time.Time{})
	if _, err := f.Write(bytes.Repeat([]byte("\n"), 4<<20)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("filling the server's input: %v", err)
	}
}
