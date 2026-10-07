package proxy

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/inebotov/aish/internal/rpc"
)

// Who may start, go on with or compact a request. The shell sends these
// from __aish_ask, and the user types `aish compact`: a job of the shell in
// the foreground of its terminal, the PTY. Anything else at $AISH_SOCK — a
// background subagent's command, which outlives the request, or a job the
// agent's command left in the background — would start a request or a
// summary while the user is at the prompt, where nested finds no request
// in progress to refuse it by.
//
// It is a guard, not a boundary: a process of the shell's own session may
// make its group the foreground one (tcsetpgrp, with SIGTTOU ignored) or
// stay in the shell's group (`set +m`). A process of another session, such
// as a subagent's command, which is the proxy's child, cannot: the PTY is
// not its controlling terminal.

var errNotShell = errors.New("not from the shell's foreground: a background process cannot start, resume or compact a request, switch the session or the model, or apply the config")

// fromShell is nil if the call ctx is of comes from the process group in
// the foreground of the shell's terminal. Without the client's pid, or
// without the terminal (before Run starts the shell), it is errNotShell.
func (p *Proxy) fromShell(ctx context.Context) error {
	p.mu.Lock()
	fg := p.fg
	p.mu.Unlock()
	pid, ok := rpc.Peer(ctx)
	if !ok || fg == nil {
		return errNotShell
	}
	group, err := unix.Getpgid(pid)
	if err != nil {
		return errNotShell
	}
	want, err := fg()
	if err != nil || want <= 0 || group != want {
		return errNotShell
	}
	return nil
}

// setTerminal makes ptmx, the master side of the shell's PTY, tell
// fromShell its foreground process group.
func (p *Proxy) setTerminal(ptmx *os.File) {
	fg := func() (int, error) {
		// Not ptmx.Fd(), which would put the PTY in blocking mode.
		raw, err := ptmx.SyscallConn()
		if err != nil {
			return 0, err
		}
		var group int
		var gerr error
		if err := raw.Control(func(fd uintptr) { group, gerr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) }); err != nil {
			return 0, err
		}
		return group, gerr
	}
	p.mu.Lock()
	p.fg = fg
	p.mu.Unlock()
}
