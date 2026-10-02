package proxy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

func TestWaitInterrupted(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = io.Discard
	p.marker(Marker{Kind: "ask-start"})
	p.marker(Marker{Kind: "agent-start", Payload: "c1;sleep 100"})
	p.output([]byte("partial\r\n"))

	type result struct {
		out rpc.Output
		err error
	}
	res := make(chan result, 1)
	go func() {
		out, err := p.wait(context.Background(), "c1", time.Minute)
		res <- result{out, err}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		_, waiting := p.waiters["c1"]
		p.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wait_output never started waiting")
		}
	}

	// Ctrl+C: back at the prompt without agent-end.
	p.marker(Marker{Kind: "cmd-end", Payload: "130;/tmp"})
	select {
	case r := <-res:
		if r.err != nil || r.out.Exit != 130 || !strings.Contains(r.out.Output, "partial") {
			t.Fatalf("got %+v, %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait_output still waits after cmd-end")
	}
	if len(p.waiters) != 0 || len(p.done) != 0 {
		t.Errorf("left waiters %v, done %v", p.waiters, p.done)
	}
}

func TestWaitTimeout(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	if _, err := p.wait(context.Background(), "c1", time.Millisecond); err == nil {
		t.Fatal("no error without output")
	}
	if len(p.waiters) != 0 {
		t.Errorf("left waiters %v", p.waiters)
	}
}

// An agent that gave up waiting leaves no waiter behind.
func TestWaitCancel(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.wait(ctx, "c1", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}
	if len(p.waiters) != 0 {
		t.Errorf("waiters left: %v", p.waiters)
	}
}
