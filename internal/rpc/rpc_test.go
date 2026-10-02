package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
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
