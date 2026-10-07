package proxy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// journal is the proxy's session for the agent: the session object changes
// with `aish resume`, so it is looked up at each call.
type journal struct{ p *Proxy }

func (j journal) ID() string {
	j.p.mu.Lock()
	defer j.p.mu.Unlock()
	return j.p.sess.ID
}
func (j journal) Len() int                         { return j.p.session().Len() }
func (j journal) Entries() []session.Entry         { return j.p.session().Entries() }
func (j journal) Append(es ...session.Entry) error { return j.p.session().Append(es...) }

// shell is the user's bash for the agent: a command is left in $AISH_RUN
// for __aish_ask to run, and its output comes back with the agent-end
// marker.
type shell struct{ p *Proxy }

// HandOff marks the command handed before the shell can run it: a request
// from the command itself (errNested) may come before its agent-start.
func (s shell) HandOff(id, cmd string) error {
	if err := os.WriteFile(filepath.Join(s.p.run, "next.id"), []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.p.run, "next.cmd"), []byte(cmd), 0o600); err != nil {
		return err
	}
	s.p.mu.Lock()
	s.p.handed = id
	s.p.mu.Unlock()
	return nil
}

func (s shell) Wait(ctx context.Context, id string, timeout time.Duration) (rpc.Output, error) {
	out, err := s.p.wait(ctx, id, timeout)
	// With its output or without: agent_resume comes once the shell is
	// done with the command, and the agent goes on either way.
	s.p.mu.Lock()
	if s.p.handed == id {
		s.p.handed = ""
	}
	s.p.mu.Unlock()
	return out, err
}

// ui is the terminal for the agent. Everything goes through emit under
// p.mu, like the shell's output, so Ctrl+O cannot interleave with it. The
// terminal is raw: a bare newline would not return the carriage, which the
// PTY used to do for the agent.
type ui struct {
	p  *Proxy
	cr crlf
}

func (u *ui) Write(b []byte) (int, error) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	u.p.waits = false // the agent has its line back
	u.p.stopSpin()
	u.p.emit(u.cr.fix(b))
	return len(b), nil
}

func (u *ui) Size() (int, int) {
	if u.p.size == nil {
		return 0, 0
	}
	return u.p.size()
}

func (u *ui) Ask(ctx context.Context, q string) (string, error) { return u.p.askUser(ctx, q) }

func (u *ui) Form(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	return u.p.askForm(ctx, qs)
}

// Fold shows the status of a result as a command's is shown: drawn by the
// same code, on the line of the call if it was left open. The text is
// kept as it came, whole.
func (u *ui) Fold(title, text string) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	f := newResult(title, text)
	f.at, u.p.at = u.p.at, nil
	u.p.emit(f.finish(-1))
	u.p.folds = append(u.p.folds, Fold{Title: title, Text: text})
}

func (u *ui) Live(title string) agent.Live {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	l := &live{p: u.p}
	at := u.p.at
	u.p.at = nil
	if u.p.foldLines >= 0 {
		l.f = newFold(title, u.p.foldLines)
		u.p.tool = l.f
	}
	if l.f != nil && u.p.foldLines == 0 {
		l.f.at = at
	} else if at != nil {
		u.p.emit([]byte("\r\n")) // the output shows below the call
	}
	return l
}

// Hidden keeps a call the agent summed up in its line (hide_work) for
// Ctrl+O, as a folded result is kept; nothing is drawn.
func (u *ui) Hidden(title, text string) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	u.p.folds = append(u.p.folds, Fold{Title: title, Text: text})
}

// HideCommand has the command the shell runs next folded quietly: the
// agent's line of calls stays the last on the screen, turning (line, see
// hidework.go), until the agent goes on with it. If it does not, the
// command cut short, cmd-end ends that line.
func (u *ui) HideCommand(line func(n, cols int) string) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	u.p.hide, u.p.waits = true, true
	u.p.startSpin(line)
}

// CommandAt needs no long: the status goes at the right edge of the last
// line, however many the call took.
func (u *ui) CommandAt(col int, _ bool, hidden int) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	if u.p.size == nil {
		return
	}
	if w, _ := u.p.size(); col >= 0 && w > 0 {
		u.p.at = &statusAt{col: min(col, w), cols: w, hidden: hidden}
	}
}

// live is an external tool's output as it runs, folded like a command's;
// without folding (fold_lines < 0) it passes through, and Finish ends the
// line it left open, as the fold's finish does.
type live struct {
	p    *Proxy
	f    *fold
	cr   crlf
	last lastLine // without a fold
}

func (l *live) Write(b []byte) (int, error) {
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	show := l.cr.fix(b)
	if l.f != nil {
		show = l.f.write(show)
	} else {
		l.last.feed(show)
	}
	l.p.emit(show)
	return len(b), nil
}

func (l *live) Finish(exit int) {
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	if l.f == nil && l.last.text {
		l.last.feed([]byte("\r\n"))
		l.p.emit([]byte("\r\n"))
	}
	if l.f != nil && l.p.tool == l.f {
		l.p.finishFold(l.f, exit)
		l.p.tool = nil
	}
}

// crlf turns each newline into a carriage return and newline, across
// writes, unless it has one already.
type crlf struct{ cr bool }

func (c *crlf) fix(b []byte) []byte {
	if !bytes.Contains(b, []byte{'\n'}) {
		if len(b) > 0 {
			c.cr = b[len(b)-1] == '\r'
		}
		return b
	}
	out := make([]byte, 0, len(b)+bytes.Count(b, []byte{'\n'}))
	for _, ch := range b {
		if ch == '\n' && !c.cr {
			out = append(out, '\r')
		}
		out = append(out, ch)
		c.cr = ch == '\r'
	}
	return out
}
