package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/session"
)

func serveTest(t *testing.T, h Handler) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go Serve(l, h)
	return &Client{Path: path}
}

func TestCall(t *testing.T) {
	c := serveTest(t, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "fail" {
			return nil, errors.New("no luck")
		}
		return string(params), nil
	})
	var got string
	if err := c.Call("echo", 42, &got); err != nil || got != "42" {
		t.Fatalf("echo: %q %v", got, err)
	}
	if err := c.Call("fail", nil, nil); err == nil || err.Error() != "no luck" {
		t.Fatalf("fail: %v", err)
	}
}

// A client that gives up on a hung proxy gets an error in time, and the
// proxy learns of it.
func TestGiveUp(t *testing.T) {
	stopped := make(chan struct{}, 1)
	c := serveTest(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		stopped <- struct{}{}
		return nil, ctx.Err()
	})
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, context.DeadlineExceeded},
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, context.Canceled},
	} {
		ctx, cancel := tc.ctx()
		start := time.Now()
		err := c.CallContext(ctx, MethodInfo, nil, nil)
		cancel()
		if !errors.Is(err, tc.want) || time.Since(start) > 5*time.Second {
			t.Errorf("%s: %v after %v", tc.name, err, time.Since(start))
		}
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the handler goes on", tc.name)
		}
	}
}

type call struct {
	method string
	params json.RawMessage
}

// record serves h and records the calls.
func record(t *testing.T, h Handler) (*Client, func() []call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	c := serveTest(t, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		mu.Lock()
		calls = append(calls, call{method, params})
		mu.Unlock()
		return h(ctx, method, params)
	})
	return c, func() []call {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func TestCallEncoding(t *testing.T) {
	c, calls := record(t, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case MethodInfo:
			return Info{SessionID: "s1", Model: "m", Window: 1000}, nil
		case MethodModel:
			var p ModelParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, errors.New("unknown method " + method)
	})

	var info Info
	if err := c.Call(MethodInfo, nil, &info); err != nil {
		t.Fatal(err)
	}
	if info != (Info{SessionID: "s1", Model: "m", Window: 1000}) {
		t.Errorf("info %+v", info)
	}
	if got := calls()[0]; got.method != MethodInfo || got.params != nil {
		t.Errorf("no params should arrive as nil: %q", got.params)
	}

	if err := c.Call(MethodModel, ModelParams{Model: "m2"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := string(calls()[1].params); got != `{"model":"m2","window":0}` {
		t.Errorf("params %s", got)
	}

	// A nil result is sent as null and leaves the value alone.
	info = Info{SessionID: "kept"}
	if err := c.Call(MethodModel, ModelParams{}, &info); err != nil || info.SessionID != "kept" {
		t.Errorf("%+v, %v", info, err)
	}

	err := c.Call("nope", nil, &info)
	if err == nil || err.Error() != "unknown method nope" {
		t.Errorf("handler error: %v", err)
	}
	if info.SessionID != "kept" {
		t.Errorf("an error changed the result: %+v", info)
	}

	// The result does not fit what the caller expects.
	var n int
	if err := c.Call(MethodInfo, nil, &n); err == nil {
		t.Error("decoding Info into an int should fail")
	}
}

func TestBadRequest(t *testing.T) {
	c, calls := record(t, func(context.Context, string, json.RawMessage) (any, error) { return "ok", nil })
	for _, req := range []string{"{not json\n", ""} {
		conn, err := net.Dial("unix", c.Path)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(conn, req)
		conn.(*net.UnixConn).CloseWrite()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		// The server hangs up without an answer.
		if b, err := io.ReadAll(conn); err != nil || len(b) != 0 {
			t.Errorf("%q: got %q, %v", req, b, err)
		}
		conn.Close()
	}
	if n := len(calls()); n != 0 {
		t.Errorf("handler called %d times", n)
	}
	// The server is still there.
	var s string
	if err := c.Call(MethodInfo, nil, &s); err != nil || s != "ok" {
		t.Errorf("after bad requests: %q, %v", s, err)
	}
}

func TestConcurrent(t *testing.T) {
	release := make(chan struct{})
	c, _ := record(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == MethodAgentStart {
			<-release
		}
		return method, nil
	})
	done := make(chan error)
	go func() {
		var s string
		done <- c.Call(MethodAgentStart, nil, &s)
	}()
	// A call the proxy works on for long does not hold up the others.
	var s string
	if err := c.Call(MethodInfo, nil, &s); err != nil || s != MethodInfo {
		t.Errorf("%q, %v", s, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Error(err)
	}
}

func TestHelpers(t *testing.T) {
	c, calls := record(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case MethodHistory:
			return []session.Entry{{Kind: session.KindUser, Text: "hi"}}, nil
		}
		return nil, nil
	})
	es, err := c.History()
	if err != nil || len(es) != 1 || es[0].Text != "hi" {
		t.Errorf("history %+v, %v", es, err)
	}
	if err := c.Call(MethodAgentResume, AgentParams{ID: "c1", RC: 2, Cwd: "/tmp"}, nil); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if got[1].method != MethodAgentResume || string(got[1].params) != `{"id":"c1","rc":2,"cwd":"/tmp"}` {
		t.Errorf("agent_resume: %s %s", got[1].method, got[1].params)
	}
}

func TestServeStops(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		Serve(l, nil)
		close(done)
	}()
	l.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the listener closed")
	}
}

func TestClientErrors(t *testing.T) {
	c := &Client{Path: filepath.Join(t.TempDir(), "none")}
	if err := c.Call(MethodInfo, nil, nil); err == nil {
		t.Error("no socket: no error")
	}

	t.Setenv("AISH_SOCK", "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "AISH_SOCK is not set") {
		t.Errorf("FromEnv without AISH_SOCK: %v", err)
	}
	t.Setenv("AISH_SOCK", "/run/aish/sock")
	if c, err := FromEnv(); err != nil || c.Path != "/run/aish/sock" {
		t.Errorf("FromEnv: %+v, %v", c, err)
	}
}
