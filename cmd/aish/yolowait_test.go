package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// askingProxy answers rpc yolo with answer, which gets the call's ctx: the
// proxy's question waits for the user so.
func askingProxy(t *testing.T, answer func(ctx context.Context) error) {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go rpc.Serve(l, func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		if method != rpc.MethodYolo {
			t.Errorf("rpc %s", method)
		}
		return nil, answer(ctx)
	})
	t.Setenv("AISH_SOCK", l.Addr().String())
}

// aish yolo waits for the answer to the proxy's question past
// rpc.CallTimeout, which bounds the calls answered at once. The timeout is
// the real one, so the test takes its ten seconds.
func TestYoloWaitsForAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out rpc.CallTimeout")
	}
	diskConfig(t, "")
	askingProxy(t, func(ctx context.Context) error {
		select {
		case <-time.After(rpc.CallTimeout + 500*time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	code, stdout, stderr := captured(t, func() int { return run([]string{"yolo"}) })
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "yolo: no policies") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// Ctrl+C gives up on the question: the call ends in the proxy, which takes
// the question off the screen before it answers, and only then aish yolo
// says the checks stay on.
func TestYoloInterrupted(t *testing.T) {
	diskConfig(t, "")
	erased := make(chan struct{})
	askingProxy(t, func(ctx context.Context) error {
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT) // the client is waiting, its handler set
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			return errors.New("the call went on in the proxy")
		}
		time.Sleep(50 * time.Millisecond) // the question going off the screen
		close(erased)
		return ctx.Err()
	})
	code, stdout, stderr := captured(t, func() int { return run([]string{"yolo"}) })
	if code != 130 || stdout != "" || !strings.Contains(stderr, "interrupted, the checks stay on") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	select {
	case <-erased:
	default:
		t.Error("aish yolo went on before the proxy answered")
	}
}

// Ctrl+C after the proxy took the Yes: aish yolo says what is so, yolo on.
func TestYoloInterruptedAfterYes(t *testing.T) {
	diskConfig(t, "")
	askingProxy(t, func(ctx context.Context) error {
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(50 * time.Millisecond) // the Yes taken as the client gave up
		return nil
	})
	code, stdout, stderr := captured(t, func() int { return run([]string{"yolo"}) })
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "yolo: no policies") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
