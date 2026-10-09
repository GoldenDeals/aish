package proxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/GoldenDeals/aish/internal/agent"
)

// Esc during a request stops what the agent does now, the request going
// on, while Ctrl+C ends the whole request. A call in the proxy, a tool or
// task with its subagents, is stopped by the agent (Agent.Interrupt): its
// result is what it printed so far, "[interrupted by the user]" after it.
// The agent's command the shell runs is stopped by the shell: the proxy
// names the call in $AISH_RUN/esc and sends SIGINT to the foreground of
// the shell's terminal, as Ctrl+C would; the shell ends the command with
// agent-end and the code from the file and resumes the agent
// (__aish_escaped in init.bash, the always block of __aish_run in
// init.zsh). A turn of the model ends the request, quietly (errStopped).
//
// The keys of the viewer, the panes, a question and the form keep Esc: it
// reaches takeKeys's escKeys only past them. A program the agent's command
// runs that has the terminal, full-screen or reading the keys as they come
// (an editor, fzf), gets Esc as before.

const escByte = 0x1b

// escKey is a lone ESC held at the end of a read during a request: the
// read may have cut a key's sequence after it. Under p.mu.
type escKey struct {
	held  bool
	gen   int         // which held ESC the timer is for
	timer *time.Timer // takes it for Esc after escWait
}

// cmdStop is the agent's command the shell was asked to stop.
type cmdStop struct {
	id string
	in *agent.Interruption
}

// errStopped is the cause Esc ends a request with between its calls, in a
// turn of the model: the request is over, as with Ctrl+C, but it is no
// error, and the agent waits for the user as when the model is done.
var errStopped = errors.New("stopped by the user")

// escKeys takes a lone Esc out of b, keys on their way to the shell: during
// a request it stops what the agent does (escape). An ESC that ends the
// read waits for escWait: what comes right after it makes it the start of
// a key's sequence, Alt+B or an arrow, which goes on to the shell whole.
// Called under p.mu.
func (p *Proxy) escKeys(b []byte) []byte {
	e := &p.esc
	if e.held {
		e.held = false
		e.timer.Stop()
		b = append([]byte{escByte}, b...)
	}
	if len(b) == 0 || b[len(b)-1] != escByte || !p.escapes() {
		return b
	}
	e.held = true
	e.gen++
	gen := e.gen
	e.timer = time.AfterFunc(escWait, func() { p.loneEsc(gen) })
	return b[:len(b)-1]
}

// loneEsc takes the ESC held as gen for Esc, nothing having come after it.
// Pressed during the request, it goes nowhere if the request is over by now.
func (p *Proxy) loneEsc(gen int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := &p.esc
	if !e.held || e.gen != gen {
		return // a read took it
	}
	e.held = false
	if p.escapes() && !p.readsKeys() {
		p.escape()
	}
}

// escapes reports whether Esc is aish's: a request is in progress, and no
// program of the agent's command has the terminal — on the alternate
// screen, or with the terminal out of the line mode the shell runs
// commands in. Called under p.mu.
func (p *Proxy) escapes() bool {
	if !p.asking || p.early != nil {
		return false
	}
	if len(p.agent) > 0 && (p.screen.alt || !p.readsLines()) {
		return false
	}
	return true
}

// readsLines reports whether the shell's terminal is in canonical mode, as
// the shell leaves it for a command: nobody reads the keys one by one.
// Without a terminal, or when it cannot tell, it is true. Called under p.mu.
func (t *console) readsLines() bool {
	if t.lines == nil {
		return true
	}
	return t.lines()
}

// escape stops what the request does now: the agent's command the shell
// runs, or is about to run; the call in progress in the proxy; the turn of
// the model, which ends the request. Called under p.mu.
func (p *Proxy) escape() {
	if p.handed != "" && p.interruptCmd(p.handed, agent.ByUser) {
		return
	}
	if p.reqCtx == nil || p.reqCtx.Err() != nil {
		return
	}
	if p.ag != nil && p.ag.Interrupt(agent.ByUser) {
		return
	}
	if p.stopReq != nil {
		p.stopReq(errStopped)
	}
}

// interruptCmd has the shell stop the agent's command for call id, for in:
// $AISH_RUN/esc names the call and the code the command ends with, and
// SIGINT goes to the foreground of the shell's terminal. A command not
// started yet gets the signal at its agent-start (stopStarted); one asked
// again gets it again. It is false when the shell is done with the
// command: its output waits for the agent. Called under p.mu.
func (p *Proxy) interruptCmd(id string, in *agent.Interruption) bool {
	if _, done := p.done[id]; done || p.run == "" {
		return false
	}
	if p.stop == nil || p.stop.id != id {
		line := fmt.Appendf(nil, "%s %d\n", id, in.Code)
		if err := os.WriteFile(filepath.Join(p.run, "esc"), line, 0o600); err != nil {
			return false
		}
		p.stop = &cmdStop{id: id, in: in}
	}
	if p.agent[id] != nil {
		p.signalShell()
	}
	return true
}

// signalShell sends SIGINT to the foreground of the shell's terminal: the
// agent's command, or the shell itself in a loop of its own. Called under
// p.mu.
func (p *Proxy) signalShell() {
	if p.fg == nil {
		return
	}
	if g, err := p.fg(); err == nil && g > 0 && g != unix.Getpgrp() {
		_ = unix.Kill(-g, unix.SIGINT)
	}
}

// stopStarted sends the stop asked for before the shell started the
// command for call id. Called under p.mu.
func (p *Proxy) stopStarted(id string) {
	if p.stop != nil && p.stop.id == id {
		p.signalShell()
	}
}

// stopEnded is why the command for call id, ended now, was stopped; "" if
// it was not. Called under p.mu.
func (p *Proxy) stopEnded(id string) string {
	s := p.stop
	if s == nil || s.id != id {
		return ""
	}
	p.dropStop()
	return s.in.Why
}

// dropStop forgets the stop: the command is over, or the request.
// Called under p.mu.
func (p *Proxy) dropStop() {
	if p.stop == nil {
		return
	}
	p.stop = nil
	_ = os.WriteFile(filepath.Join(p.run, "esc"), nil, 0o600)
}

// Interrupt stops the agent's command for call id the way Esc does, for in
// (agent.Interrupter).
func (s shell) Interrupt(id string, in *agent.Interruption) bool {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	return s.p.interruptCmd(id, in)
}

var _ agent.Interrupter = shell{}

// lineMode tells whether the shell's terminal is in canonical mode.
type lineMode func() bool

// termLines is the lineMode of ptmx, the master side of the shell's PTY.
func termLines(ptmx *os.File) lineMode {
	return func() bool {
		// Not ptmx.Fd(), which would put the PTY in blocking mode.
		raw, err := ptmx.SyscallConn()
		if err != nil {
			return true
		}
		lines := true
		_ = raw.Control(func(fd uintptr) {
			if c, err := canonical(int(fd)); err == nil {
				lines = c
			}
		})
		return lines
	}
}
