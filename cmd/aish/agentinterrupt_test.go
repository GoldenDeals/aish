package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// A request stopped by Ctrl+C leaves nothing on the screen of its own: the
// agent has already said what it had to, and the shell only needs the code.
func TestAgentInterruptQuiet(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cancelled := make(chan struct{})
	go rpc.Serve(l, func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case rpc.MethodAgentStart:
			// The client listens for signals before it calls.
			if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
				return nil, err
			}
			select {
			case <-cancelled:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(giveUpAfter):
				return nil, errors.New("no agent_cancel")
			}
		case rpc.MethodAgentCancel:
			close(cancelled)
			return nil, nil
		}
		t.Errorf("unexpected rpc %s", method)
		return nil, errors.New("unexpected")
	})
	t.Setenv("AISH_SOCK", l.Addr().String())

	code, out, errOut := captured(t, func() int { return agentCmd([]string{"start", "--", "hi"}) })
	if code != 130 {
		t.Errorf("exit code %d, want 130", code)
	}
	select {
	case <-cancelled:
	default:
		t.Error("agent_cancel was not called")
	}
	if out != "" || errOut != "" {
		t.Errorf("stdout %q, stderr %q, want nothing", out, errOut)
	}
}
