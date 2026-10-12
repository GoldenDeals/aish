// Package bash gives the model a bash tool: it runs a command in the working directory and returns its stdout
// and stderr together. The command has no stdin and is killed when the user stops the turn.
// Nothing asks before a command runs: build a policy module in to confirm them.
package bash

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/modules/tools/internal/serve"
)

var spec = tool.Spec{
	Name: "bash",
	Description: "Run a bash command in the working directory and get its stdout and stderr together. " +
		"Each call is a new shell: cd and variables don't carry over. There is no stdin, so use non-interactive flags. " +
		"Long outputs are cut in the middle: filter them with grep, head or tail.",
	Schema: []byte(`{"type":"object","properties":{"command":{"type":"string","description":"The command to run"}},"required":["command"]}`),
}

func init() { serve.Register("tools.bash", spec, run) }

func run(ctx context.Context, in struct{ Command string }) (string, error) {
	if in.Command == "" {
		return "", errors.New("command is empty")
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", in.Command)
	// shortcut: only bash is killed, its children live on; kill the process group if they pile up
	cmd.WaitDelay = time.Second // a child left in the background holds the output open: don't wait for it
	out, err := cmd.CombinedOutput()
	switch {
	case ctx.Err() != nil:
		err = errors.New("interrupted")
	case errors.Is(err, exec.ErrWaitDelay):
		err = nil // bash exited fine, the child is still running
	}
	return string(out), err
}
