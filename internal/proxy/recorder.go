package proxy

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

const (
	headCap = 64 << 10
	tailCap = 64 << 10
)

type segment struct {
	cmd     string
	buf     *capture.Buffer
	fold    *fold // agent commands only
	cleared bool  // the command erased the screen

	// last follows the line an agent command's output leaves open when it
	// has no fold, as the fold does: the end of the command ends that line,
	// which the spinner of the next turn (it starts with \r and erases the
	// line) or the prompt would take.
	last lastLine
}

// Fold is one output hidden behind "ctrl+o to expand".
type Fold = rpc.Fold

// recorder follows the shell's output by the markers around it
// (markers.go): a command the user typed goes to the journal with what it
// printed, an agent's command to the agent, which waits for it in wait;
// the output of an agent's command or of an external tool is folded on
// the screen. Under p.mu.
type recorder struct {
	asking bool                // inside __aish_ask, between ask-start and the next prompt
	user   *segment            // command typed by the user, between cmd-start and cmd-end
	agent  map[string]*segment // commands run on behalf of the agent, by call id
	tool   *fold               // live output of an external tool, while it runs
	at     *statusAt           // where the agent left the cursor after printing its next command
	hide   bool                // the agent's next command is not to be drawn (hide_work), see ui.HideCommand
	waits  bool                // and its line of calls is left open for that command, until the agent writes
	spin   *spin               // that line, kept turning while the command runs, see hidework.go
	watch  *promptWatch        // the fold of another command, watched for a prompt, see foldprompt.go
	folds  []Fold              // folded outputs of the last request, for Ctrl+O

	done    map[string]rpc.Output    // outputs of the agent's commands, till wait takes them
	waiters map[string]chan struct{} // waits for those yet to come
}

// liveFold is the fold of the output being printed right now, if any.
func (r *recorder) liveFold() *fold {
	if r.tool != nil {
		return r.tool
	}
	for _, s := range r.agent {
		if s.fold != nil {
			return s.fold
		}
	}
	return nil
}

// pump copies PTY output to the terminal, stripping markers and feeding
// the recorder.
func (p *Proxy) pump(r io.Reader, f *Filter) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			f.Feed(buf[:n], p.output, p.marker)
		}
		if err != nil {
			return
		}
	}
}

// output records b and shows it, folded if it belongs to a long agent output.
// The terminal is written under the lock so Ctrl+O cannot interleave.
func (p *Proxy) output(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.earlyOutput(b)
	show := b
	if cleared, from := p.screen.Feed(b); cleared && !p.asking {
		p.cleared()
		if p.user != nil {
			p.user.buf.Write(b[from:])
		}
	} else if p.user != nil {
		p.user.buf.Write(b)
	}
	for _, s := range p.agent {
		s.buf.Write(b)
		if s.fold != nil {
			show = s.fold.write(b)
		} else {
			s.last.feed(b)
		}
	}
	if p.tool != nil {
		show = p.tool.write(b)
	}
	if !p.asking {
		// A request ends with the agent's own output, which ends its line.
		p.col.feed(b)
	}
	if p.line != nil {
		show = p.line.feed(show)
	}
	p.emit(show)
	p.pasteOutput(b, show)
}

// cleared marks in the journal where the user erased the screen: the
// session goes on, and the assistant sees only what is on the screen from
// here. Two clears in a row, or one with nothing before it, cut nothing more.
func (p *Proxy) cleared() {
	if es := p.sess.Entries(); len(es) > 0 && es[len(es)-1].Kind != session.KindClear {
		_ = p.sess.Append(session.Entry{Kind: session.KindClear})
	}
	p.folds = nil
	if p.user != nil {
		p.user.buf = capture.NewBuffer(headCap, tailCap)
		p.user.cleared = true
	}
}

// finish keeps an agent command's output for wait, waking the agent's
// Resume that may already be waiting for it.
func (r *recorder) finish(id string, out rpc.Output) {
	r.done[id] = out
	if ch, ok := r.waiters[id]; ok {
		close(ch)
		delete(r.waiters, id)
	}
}

// finishFold prints the final status line and keeps the output for Ctrl+O.
func (p *Proxy) finishFold(f *fold, exit int) {
	p.emit(f.finish(exit))
	if f.keep() {
		p.folds = append(p.folds, Fold{Title: f.title, Text: string(f.raw.Bytes())})
	}
}

func render(b *capture.Buffer) (string, bool) {
	if b.AltScreen() {
		return "[full-screen interactive program; output not captured]", true
	}
	return capture.Clean(b.Bytes()), false
}

func (p *Proxy) wait(ctx context.Context, id string, timeout time.Duration) (rpc.Output, error) {
	p.mu.Lock()
	if out, ok := p.done[id]; ok {
		delete(p.done, id)
		p.mu.Unlock()
		return out, nil
	}
	ch, ok := p.waiters[id]
	if !ok {
		ch = make(chan struct{})
		p.waiters[id] = ch
	}
	p.mu.Unlock()

	select {
	case <-ch:
	case <-time.After(timeout):
	case <-ctx.Done():
		// The agent is gone; any output stays for whoever asks next.
		p.mu.Lock()
		if p.waiters[id] == ch {
			delete(p.waiters, id)
		}
		p.mu.Unlock()
		return rpc.Output{}, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// The output may have come while the timer fired.
	out, ok := p.done[id]
	if !ok {
		if p.waiters[id] == ch {
			delete(p.waiters, id)
		}
		return rpc.Output{}, fmt.Errorf("no output recorded for %s", id)
	}
	delete(p.done, id)
	return out, nil
}
