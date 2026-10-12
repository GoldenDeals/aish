package bash

import (
	"context"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	start := time.Now()
	out, err := run(context.Background(), struct{ Command string }{"echo out; echo err >&2; sleep 10 &"})
	if err != nil || out != "out\nerr\n" {
		t.Errorf("run = %q, %v", out, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("waited for a child left in the background")
	}
	if _, err := run(context.Background(), struct{ Command string }{"exit 3"}); err == nil || err.Error() != "exit status 3" {
		t.Errorf("exit 3: %v", err)
	}
}
