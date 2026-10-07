package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// groupGrace is how long a user's tool has to end on SIGTERM once ctx is
// done, and how long a child it left holding its output is waited for.
const groupGrace = 2 * time.Second

// runGroup runs cmd, made by exec.CommandContext with ctx, in a process
// group of its own. Once ctx is done the whole group gets SIGTERM, and
// what is left of it after groupGrace, or once Run is back, SIGKILL: Go
// alone kills the tool only, while a child it started (sleep 30 in a
// script) holds its output, and Run waits for it. A tool that exits on its
// own leaving a child with its output is done groupGrace after, with what
// it printed; the child is left alone, a server it started, say.
func runGroup(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = groupGrace
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// What ignored SIGTERM, or held the output past groupGrace, or
		// outlived a tool that was done before ctx. The tool is reaped,
		// but its id stays the group's while any of the group is left.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
		return nil
	}
	return err
}
