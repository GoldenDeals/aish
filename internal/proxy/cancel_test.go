package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/tools"
)

// Ctrl+C on a request still waiting for the one before it (that one
// interrupted and closing) reaches the one in progress; the request
// waiting must not go on after it. A request that comes after Ctrl+C runs.
func TestCancelQueued(t *testing.T) {
	p, _, cwd := hosted(t, &scripted{})
	ex := tools.Exec{Dir: cwd}

	held, release := make(chan struct{}), make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		aDone <- p.request(context.Background(), ex, true, func(ctx context.Context, _ *agent.Agent) error {
			close(held)
			<-release
			return ctx.Err()
		})
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("request A never started")
	}

	// B comes while A has the turn: what request does before takeTurn.
	p.mu.Lock()
	gen := p.cancelGen
	p.mu.Unlock()
	bTurn := make(chan bool, 1)
	go func() { bTurn <- p.takeTurn(gen, func() {}) }()

	if _, err := call(t, p, rpc.MethodAgentCancel, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-aDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("request A ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request A goes on after agent_cancel")
	}
	select {
	case ok := <-bTurn:
		if ok {
			t.Fatal("request B, waiting at Ctrl+C, got its turn")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request B still waits")
	}

	ran := false
	err := p.request(context.Background(), ex, true, func(ctx context.Context, _ *agent.Agent) error {
		ran = true
		return ctx.Err()
	})
	if err != nil || !ran {
		t.Errorf("request C, after Ctrl+C: ran %v, err %v", ran, err)
	}
	if p.cancelReq != nil {
		t.Error("cancelReq left behind")
	}
}
