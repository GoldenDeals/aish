package rpc

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// The handler knows which process called: the proxy asks whether it is
// the shell's foreground job.
func TestPeer(t *testing.T) {
	c := serveTest(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		pid, ok := Peer(ctx)
		if !ok {
			return 0, nil
		}
		return pid, nil
	})
	var pid int
	if err := c.Call("peer", nil, &pid); err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Errorf("the client is %d, not %d", pid, os.Getpid())
	}
	if _, ok := Peer(context.Background()); ok {
		t.Error("a client without a connection")
	}
}
