package proxy

import (
	"slices"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/capture"
)

// The subagents of a task call run at once, and each gets a pane of its
// own on the alternate screen, tiled as tmux tiles them: a header with its
// title and state, the first words of its task below, then the tail of its
// output. The call opens the panes of all its subagents at once, those
// queued past maxParallel too, so the grid is laid out once for all of
// them and stays as they start. Meanwhile the screen is the layout's, as
// it is the viewer's while that is open: the output of the shell and of
// the agent waits in p.held (holding). The layout opens once the
// subagents have run paneDelay, and the call closes it when all of them
// are done: the screen comes back, a line per subagent sums it up below
// the call, and its task and whole output are kept for Ctrl+O. Subagents
// done sooner leave only those lines.

const (
	paneMinWidth = 20                    // the narrowest column the grid makes
	paneRedraw   = 50 * time.Millisecond // a frame at most this often while output comes
	paneTail     = 64 << 10              // the end of an output kept to draw its pane

	panesOpen  = "\x1b[?1049h\x1b[?25l"
	panesClose = "\x1b[?1049l\x1b[?25h"

	paneOK      = "\x1b[32m"
	paneFail    = "\x1b[31m"
	panePartial = "\x1b[33m"
)

// paneDelay is how long the subagents of a call run before the layout
// opens: one done sooner would only flash the alternate screen. A
// variable for the tests.
var paneDelay = 150 * time.Millisecond

var _ agent.Panes = (*ui)(nil)

// Pane opens a pane for a subagent of the task call, queued till it
// starts; the first one sets the layout to open once paneDelay is over.
// While the viewer has the screen, the layout waits for Ctrl+O; without a
// terminal there is nothing to draw it on, and the outputs are only kept
// and summed up at the end.
func (u *ui) Pane(title, prompt string) agent.Pane {
	p := u.p
	p.mu.Lock()
	defer p.mu.Unlock()
	ps := p.panes
	if ps == nil {
		ps = &panes{zoom: -1, call: p.tool, yolo: p.yolo}
		p.panes = ps
		if p.view == nil && p.size != nil {
			ps.delay = time.AfterFunc(paneDelay, func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.panesDue(ps)
			})
		}
	}
	pn := &pane{title: oneLine(title), prompt: prompt, buf: capture.NewBuffer(foldRawCap, foldRawCap), exit: -1}
	ps.list = append(ps.list, pn)
	if ps.shown {
		p.drawPanes()
	}
	return &paneWriter{p: p, ps: ps, pn: pn}
}

// ClosePanes ends the layout of the task call, its subagents all done.
func (u *ui) ClosePanes() {
	p := u.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.panes != nil {
		p.closePanes()
	}
}

// paneWriter is a subagent's output into its pane.
type paneWriter struct {
	p  *Proxy
	ps *panes
	pn *pane
}

func (w *paneWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	w.pn.write(b)
	if w.p.panes == w.ps && w.ps.shown {
		w.p.paneOutput()
	}
	return len(b), nil
}

// Start marks the pane running: its subagent got its turn.
func (w *paneWriter) Start() {
	p := w.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.pn.done || !w.pn.start.IsZero() {
		return
	}
	w.pn.start = time.Now()
	if p.panes == w.ps && w.ps.shown {
		p.drawPanes()
	}
}

// Outcome keeps the number of tool calls for the summary, and whether the
// answer is partial: the pane is not ok then.
func (w *paneWriter) Outcome(calls int, partial bool) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	w.pn.calls, w.pn.partial = calls, partial
}

// Finish marks the pane done. The layout stays for the call to close:
// the subagents past maxParallel come as the first ones end.
func (w *paneWriter) Finish(exit int) {
	p := w.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.pn.done {
		return
	}
	w.pn.exit, w.pn.done, w.pn.end = exit, true, time.Now()
	if p.panes == w.ps && w.ps.shown {
		p.drawPanes()
	}
}

// holding reports whether the alternate screen is taken, by the viewer or
// by the panes: the output waits in p.held meanwhile.
func (t *console) holding() bool { return t.view != nil || (t.panes != nil && t.panes.shown) }

// paneKey handles what was typed while the layout is shown. Ctrl+C goes on
// to the shell, which stops the request and the subagents with it; the
// rest is the layout's. Called under p.mu.
func (t *console) paneKey(b []byte) []byte {
	var keys, pass []byte
	for _, c := range b {
		if c == 0x03 {
			pass = append(pass, c)
		} else {
			keys = append(keys, c)
		}
	}
	if t.panes.key(keys) {
		t.detachPanes()
	} else if len(keys) > 0 {
		t.drawPanes()
	}
	return pass
}

// panesDue opens the layout of ps once paneDelay is over, unless Ctrl+O
// opened it sooner or the call closed it. Called under p.mu.
func (t *console) panesDue(ps *panes) {
	if t.panes == ps && ps.delay != nil && t.view == nil {
		t.showPanes()
	}
}

// showPanes puts the layout on the alternate screen. Called under p.mu.
func (t *console) showPanes() {
	ps := t.panes
	if ps.delay != nil {
		ps.delay.Stop() // shown sooner, by Ctrl+O: q then is not undone
		ps.delay = nil
	}
	if t.size == nil {
		return
	}
	ps.shown = true
	ps.resize(t.size())
	t.write([]byte(panesOpen))
	t.syncPaste()
	t.drawPanes()
}

// drawPanes draws the layout shown. Called under p.mu.
func (t *console) drawPanes() {
	t.panes.last = time.Now()
	t.write(t.panes.render())
}

// paneOutput draws the layout after new output, at most once a paneRedraw:
// a chatty subagent would keep the terminal busy redrawing. What comes in
// between is drawn when the interval is over, as more may not come for a
// long while. Called under p.mu.
func (p *Proxy) paneOutput() {
	ps := p.panes
	wait := paneRedraw - time.Since(ps.last)
	if wait <= 0 {
		p.drawPanes()
		return
	}
	if ps.timer == nil {
		ps.timer = time.AfterFunc(wait, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			ps.timer = nil
			if p.panes == ps && ps.shown {
				p.drawPanes()
			}
		})
	}
}

// detachPanes gives the screen back while the subagents go on, with what
// it held; Ctrl+O brings the layout back. Called under p.mu.
func (t *console) detachPanes() {
	t.panes.shown = false
	t.write([]byte(panesClose))
	t.write(t.held)
	t.held = nil
	t.syncPaste() // after what was held: the mode the shell set there is in it
}

// closePanes ends the layout: the screen comes back with what it held, a
// line per pane sums it up below the call, and its task and output go to
// the folds. Once the request is over (a second Ctrl+C let the shell go back
// to its prompt before the subagents stopped) the prompt is on the screen,
// and the outputs are only kept. Called under p.mu.
func (p *Proxy) closePanes() {
	ps := p.panes
	if ps.timer != nil {
		ps.timer.Stop()
		ps.timer = nil
	}
	if ps.delay != nil {
		ps.delay.Stop()
		ps.delay = nil
	}
	if ps.shown {
		p.detachPanes()
	}
	p.panes = nil
	if p.asking && ps.call != nil && p.tool == ps.call {
		exit := -1
		if slices.ContainsFunc(ps.list, func(pn *pane) bool { return pn.exit == 130 }) {
			exit = 130 // the call is interrupted too, its ^C in it
		}
		p.leaveCall(exit)
	}
	for _, pn := range ps.list {
		text := capture.Clean(pn.buf.Bytes())
		if p.asking {
			p.emit([]byte(pn.summary(text) + "\r\n"))
		}
		if f := pn.fold(text); f != "" {
			p.folds = append(p.folds, Fold{Title: pn.foldTitle(), Text: f})
		}
	}
}

// leaveCall ends the live output of the task call before the summaries go
// below it: the subagents wrote to their panes, not there, and its "(no
// output)" would tell wrong. What did come into it, the shell's output
// meanwhile, ends as usual. Called under p.mu.
func (p *Proxy) leaveCall(exit int) {
	f := p.tool
	p.tool = nil
	switch {
	case f.open || f.folded() || f.limit > 0:
		p.finishFold(f, exit) // its lines shown, or its status
	case f.at != nil:
		p.emit([]byte("\r\n")) // the line of the call was left open for the status
	}
}
