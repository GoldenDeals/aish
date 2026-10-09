package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GoldenDeals/aish/internal/agent"
)

// openForm is the form of ask_user while it is on the terminal; shown are
// the lines of its frame there.
type openForm struct {
	f     *form
	shown []string
	done  chan struct{}
	clock *askClock // nil: the form waits as long as its ctx
	guard askGuard  // the keys typed ahead, no answer (askguard.go)
	hold  askHold   // the shell's output meanwhile (askhold.go)
}

// askForm shows the questions and waits for the user to answer or cancel
// them, or for ctx: Ctrl+C goes to the shell, which stops the request. The
// form is drawn in place below the call: the alternate screen would hide
// what the questions are about. The cursor is hidden meanwhile; the form
// draws its own where the user types.
func (p *Proxy) askForm(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	p.mu.Lock()
	if p.size == nil {
		p.mu.Unlock()
		return nil, errors.New("no terminal")
	}
	if p.ask != nil || p.form != nil {
		p.mu.Unlock()
		return nil, errors.New("a question is open already")
	}
	of := &openForm{f: newForm(qs), done: make(chan struct{}), guard: newGuard(p.keyTime())}
	p.form = of
	of.clock = p.answerClock(ctx)
	p.at = nil // the agent closed the line of the call: no status goes there
	p.syncPaste()
	p.emit([]byte("\x1b[?25l"))
	p.drawForm()
	p.mu.Unlock()
	select {
	case <-of.done:
		return of.f.answers(), nil
	case <-ctx.Done():
	case <-of.clock.ranOut():
	}
	// The request ended, or the time: nobody answered in time, and the
	// form goes as on Ctrl+C.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.form != of {
		return of.f.answers(), nil // answered as it ended: the summary is on the screen
	}
	err, _ := of.clock.ended(ctx)
	p.closeForm()
	p.releaseKeys(&of.guard)
	p.syncPaste()
	return nil, err
}

// formKey gives what the user typed to the open form and draws it anew.
// Ctrl+C goes on to the shell and Ctrl+O opens the viewer, as with a
// question. The keys typed ahead wait for the shell (askGuard). Returns
// what goes on to the shell now: Ctrl+C, after the keys typed before it,
// and, once the form is answered, the keys typed ahead and what was typed
// after the answer, as the shell keeps what is typed ahead. Called under
// p.mu.
func (p *Proxy) formKey(b []byte) []byte {
	view := false
	if i := bytes.IndexByte(b, ctrlO); i >= 0 && len(p.viewFolds()) > 0 {
		b, view = b[:i], true // the rest would be the viewer's
	}
	of := p.form
	g := &of.guard
	var pass []byte
	pressed := false
	for i := 0; i < len(b) && !of.f.done; {
		n, k, r := nextUnit(b[i:])
		key := b[i : i+n]
		i += n
		fresh := g.take()
		switch {
		case key[0] == 0x03:
			// Interrupts the request, like anywhere else.
			pass = append(append(pass, g.release(nil)...), key[0])
			of.hold.interrupted()
		case key[0] == ctrlO:
			// Nothing to view: nothing, as before.
		case !fresh && g.ahead, k == keyRune && !of.f.takes(r):
			g.hold(key)
		default:
			g.ahead = false
			if k == keyNone {
				break // Alt with a key, a sequence the form has no use for: nothing
			}
			of.f.press(k, r)
			pressed = true
			if of.f.done {
				pass = append(pass, g.release(b[i:])...) // the rest, in the order it came
			}
		}
	}
	if of.f.done {
		p.closeForm()
	} else if pressed {
		p.drawForm()
	}
	if view {
		p.openView(p.viewFolds())
		if p.form != nil {
			p.form.clock.sync() // stands while the viewer covers the form
		}
	}
	return pass
}

// drawForm draws the open form over its last frame. Called under p.mu.
func (t *console) drawForm() {
	of := t.form
	w, h := t.size()
	frame := of.f.frame(w, h)
	t.emit([]byte(of.erase(w) + frame))
	of.shown = strings.Split(frame, "\r\n")
}

// closeForm takes the form off the screen and leaves the summary of the
// answers in its place, if there are any. The shell's output held
// meanwhile goes after them, all but the echo of a Ctrl+C, which goes
// with the frame (askhold.go). Called under p.mu.
func (t *console) closeForm() {
	of := t.form
	t.form = nil
	of.clock.stop()
	w, _ := t.size()
	out := string(of.hold.echo()) + of.erase(w)
	if s := of.f.summary(); s != "" {
		out += s + "\r\n"
	}
	t.emit([]byte(out + "\x1b[?25h"))
	t.release(&of.hold)
	close(of.done)
}

// erase goes back to the first line of the frame on the screen and clears
// the screen from there. The lines are counted at the width the terminal
// has now: narrowed, it may have wrapped them.
func (of *openForm) erase(cols int) string {
	if of.shown == nil {
		return ""
	}
	cols = max(cols, 1)
	rows := 0
	for _, l := range of.shown {
		rows += max(1, (frameWidth(l)+cols-1)/cols)
	}
	if rows == 1 {
		return "\r\x1b[K"
	}
	// The first line is erased by itself: erase below from the top-left
	// corner is a clear screen to tmux, which keeps the screen, the frame
	// with it, in its history (scroll-on-clear).
	return fmt.Sprintf("\r\x1b[%dA\x1b[K\x1b[B\x1b[J\x1b[A", rows-1)
}
