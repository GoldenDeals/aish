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
	of := &openForm{f: newForm(qs), done: make(chan struct{})}
	p.form = of
	p.at = nil // the agent closed the line of the call: no status goes there
	p.syncPaste()
	p.emit([]byte("\x1b[?25l"))
	p.drawForm()
	p.mu.Unlock()
	select {
	case <-of.done:
		return of.f.answers(), nil
	case <-ctx.Done():
		p.mu.Lock()
		if p.form == of {
			p.closeForm()
			p.syncPaste()
		}
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

// formKey gives what the user typed to the open form and draws it anew.
// Ctrl+C goes on to the shell and Ctrl+O opens the viewer, as with a
// question. Returns what goes on to the shell: Ctrl+C and, once the form
// is answered, what was typed after it, as the shell keeps what is typed
// ahead. Called under p.mu.
func (p *Proxy) formKey(b []byte) []byte {
	view := false
	if i := bytes.IndexByte(b, ctrlO); i >= 0 && len(p.viewFolds()) > 0 {
		b, view = b[:i], true // the rest would be the viewer's
	}
	var keys []byte
	for _, c := range b {
		if c != 0x03 {
			keys = append(keys, c)
		}
	}
	n, done := 0, false
	if len(keys) > 0 {
		n, done = p.form.f.feed(keys)
	}
	var pass []byte
	for i, c := range b {
		if c == 0x03 {
			pass = append(pass, c) // interrupts the request, like anywhere else
		} else if n == 0 {
			pass = append(pass, b[i:]...) // the rest, in the order it came
			break
		} else {
			n--
		}
	}
	if done {
		p.closeForm()
	} else if len(keys) > 0 {
		p.drawForm()
	}
	if view {
		w, h := p.size()
		p.view = newViewer(p.viewFolds(), w, h)
		_, _ = p.out.Write(p.view.open())
	}
	return pass
}

// drawForm draws the open form over its last frame. Called under p.mu.
func (p *Proxy) drawForm() {
	of := p.form
	w, h := p.size()
	frame := of.f.frame(w, h)
	p.emit([]byte(of.erase(w) + frame))
	of.shown = strings.Split(frame, "\r\n")
}

// closeForm takes the form off the screen and leaves the summary of the
// answers in its place, if there are any. Called under p.mu.
func (p *Proxy) closeForm() {
	of := p.form
	p.form = nil
	w, _ := p.size()
	out := of.erase(w)
	if s := of.f.summary(); s != "" {
		out += s + "\r\n"
	}
	p.emit([]byte(out + "\x1b[?25h"))
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
