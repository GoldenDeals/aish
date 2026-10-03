package proxy

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/capture"
)

// The subagents of a task call run at once, and each gets a pane of its
// own on the alternate screen, tiled as tmux tiles them: a header with its
// name and state, the tail of its output below. Meanwhile the screen is
// the layout's, as it is the viewer's while that is open: the output of
// the shell and of the agent waits in p.held (holding). The layout opens
// once the subagents have run paneDelay, and the call closes it when all
// of them are done: the screen comes back, a line per subagent sums it up
// below the call, and its whole output is kept for Ctrl+O. Subagents done
// sooner leave only those lines.

const (
	paneMinWidth = 20                    // the narrowest column the grid makes
	paneRedraw   = 50 * time.Millisecond // a frame at most this often while output comes
	paneTail     = 64 << 10              // the end of an output kept to draw its pane

	panesOpen  = "\x1b[?1049h\x1b[?25l"
	panesClose = "\x1b[?1049l\x1b[?25h"

	paneOK   = "\x1b[32m"
	paneFail = "\x1b[31m"
)

// paneDelay is how long the subagents of a call run before the layout
// opens: one done sooner would only flash the alternate screen. A
// variable for the tests.
var paneDelay = 150 * time.Millisecond

var _ agent.Panes = (*ui)(nil)

// paneSep divides the columns of the grid; where the locale makes the box
// line two columns wide it would break the layout.
var paneSep = func() string {
	if runewidth.StringWidth("│") == 1 {
		return "│"
	}
	return "|"
}()

// pane is one subagent's output while it runs.
type pane struct {
	title string
	buf   *capture.Buffer // bounded: a chatty subagent must not grow forever
	tail  []byte          // the end of it from the start of a line: what the pane shows
	exit  int             // -1 while it runs
	done  bool
}

// panes is the layout of live subagent outputs on the alternate screen.
type panes struct {
	list  []*pane
	zoom  int  // index of the pane shown alone, -1 for the grid
	shown bool // the alternate screen is ours right now
	w, h  int
	last  time.Time   // when the layout was drawn, for throttling
	timer *time.Timer // draws what came since last once paneRedraw is over
	// delay opens the layout once paneDelay is over; nil once it is open,
	// and for a layout that waits for Ctrl+O.
	delay *time.Timer
	// call is the live output of the task call, which the panes stand in
	// for: the subagents write nothing there, and closePanes ends it.
	call *fold
}

// Pane opens a pane for a subagent of the task call; the first one sets
// the layout to open once paneDelay is over. While the viewer has the
// screen, the layout waits for Ctrl+O; without a terminal there is nothing
// to draw it on, and the outputs are only kept and summed up at the end.
func (u *ui) Pane(title string) agent.Live {
	p := u.p
	p.mu.Lock()
	defer p.mu.Unlock()
	ps := p.panes
	if ps == nil {
		ps = &panes{zoom: -1, call: p.tool}
		p.panes = ps
		if p.view == nil && p.size != nil {
			ps.delay = time.AfterFunc(paneDelay, func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.panesDue(ps)
			})
		}
	}
	pn := &pane{title: oneLine(title), buf: capture.NewBuffer(foldRawCap, foldRawCap), exit: -1}
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

// Finish marks the pane done. The layout stays for the call to close:
// the subagents past maxParallel come as the first ones end.
func (w *paneWriter) Finish(exit int) {
	p := w.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.pn.done {
		return
	}
	w.pn.exit, w.pn.done = exit, true
	if p.panes == w.ps && w.ps.shown {
		p.drawPanes()
	}
}

func (pn *pane) write(b []byte) {
	pn.buf.Write(b)
	pn.tail = append(pn.tail, b...)
	if len(pn.tail) > 2*paneTail {
		t := pn.tail[len(pn.tail)-paneTail:]
		if i := bytes.IndexByte(t, '\n'); i >= 0 {
			t = t[i+1:]
		}
		pn.tail = append(pn.tail[:0], t...)
	}
}

func (pn *pane) state() string {
	switch {
	case !pn.done:
		return "running"
	case pn.exit == 0:
		return "ok"
	case pn.exit == 130:
		return "interrupted"
	case pn.exit < 0:
		return "done"
	}
	return fmt.Sprintf("rc %d", pn.exit)
}

// rows are the last h rows of the output wrapped at w columns.
func (pn *pane) rows(w, h int) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	text := capture.Clean(pn.tail)
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < h; i-- {
		out = append(hardWrap(strings.ReplaceAll(lines[i], "\t", "        "), w), out...)
	}
	return out[max(0, len(out)-h):]
}

// summary is the line a pane leaves below the call once the layout is
// gone; text is its whole output.
func (pn *pane) summary(text string) string {
	mark := paneFail + "✗" + reset
	if pn.done && pn.exit == 0 {
		mark = paneOK + "✓" + reset
	}
	var parts []string
	switch n := strings.Count(strings.TrimSpace(text), "\n") + 1; {
	case strings.TrimSpace(text) == "":
		parts = append(parts, "no output")
	case n == 1:
		parts = append(parts, "1 line")
	default:
		parts = append(parts, fmt.Sprintf("%d lines", n))
	}
	if s := pn.state(); s != "ok" {
		parts = append(parts, s)
	}
	if text != "" {
		parts = append(parts, "ctrl+o to expand")
	}
	return fmt.Sprintf("  %s %s  %s(%s)%s", mark, pn.title, dim, strings.Join(parts, " · "), reset)
}

// hardWrap cuts s into rows of w columns at most, as the terminal wraps a
// long line; a rune wider than a row takes one of its own.
func hardWrap(s string, w int) []string {
	var out []string
	start, width := 0, 0
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if width+rw > w && i > start {
			out = append(out, s[start:i])
			start, width = i, 0
		}
		width += rw
	}
	return append(out, s[start:])
}

// cellText is s cut and padded to w columns: cells stand side by side, so
// no row may end short or run into the next one.
func cellText(s string, w int) string {
	return runewidth.FillRight(runewidth.Truncate(s, w, ""), w)
}

// cell is where a pane goes on the screen: its top left corner, from 0,
// and its size.
type cell struct{ x, y, w, h int }

// grid tiles n panes on a w×h screen whose last line is the status bar:
// as square as the columns fit, none narrower than paneMinWidth unless the
// screen is. The room is split evenly, what is left going to the last row
// and column, and the last pane of a short last row takes the rest of it:
// the cells cover the screen, so a frame redraws all of it.
func grid(n, w, h int) (cols, rows int, cells []cell) {
	if n <= 0 {
		return 0, 0, nil
	}
	for cols*cols < n {
		cols++
	}
	cols = max(1, min(cols, w/paneMinWidth))
	rows = (n + cols - 1) / cols
	area := max(h-1, 0)
	cw, ch := w/cols, area/rows
	for i := range n {
		r, c := i/cols, i%cols
		cl := cell{x: c * cw, y: r * ch, w: cw, h: ch}
		if c == cols-1 || i == n-1 {
			cl.w = w - cl.x
		}
		if r == rows-1 {
			cl.h = area - cl.y
		}
		cells = append(cells, cl)
	}
	return cols, rows, cells
}

func (ps *panes) resize(w, h int) { ps.w, ps.h = max(w, 1), max(h, 1) }

// render draws the whole layout. Every cell is drawn in full, padded to
// its width, so that a frame needs no clearing; autowrap is off meanwhile,
// so that the last column moves nothing.
func (ps *panes) render() []byte {
	var b bytes.Buffer
	b.WriteString("\x1b[?7l")
	if ps.zoom >= 0 && ps.zoom < len(ps.list) {
		ps.drawPane(&b, ps.zoom, cell{0, 0, ps.w, ps.h})
	} else {
		_, _, cells := grid(len(ps.list), ps.w, ps.h)
		for i, c := range cells {
			ps.drawPane(&b, i, c)
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s%s%s", ps.h, reverse, cellText(ps.bar(), ps.w), reset)
	}
	b.WriteString("\x1b[?7h")
	return b.Bytes()
}

// drawPane draws pane i in c: its header, then the tail of its output. A
// pane zoomed has the keys in its header, as there is no status bar then.
func (ps *panes) drawPane(b *bytes.Buffer, i int, c cell) {
	if c.w <= 0 || c.h <= 0 {
		return // a screen too small for all of them
	}
	pn := ps.list[i]
	w, sep := c.w, ""
	if c.x+c.w < ps.w {
		w, sep = c.w-1, dim+paneSep+reset
	}
	head := fmt.Sprintf(" %d %s  %s", i+1, pn.title, pn.state())
	if ps.zoom == i {
		head += "   0 grid  q detach  ctrl+c stop"
	}
	rows := pn.rows(w, c.h-1)
	for r := range c.h {
		fmt.Fprintf(b, "\x1b[%d;%dH", c.y+r+1, c.x+1)
		switch {
		case r == 0:
			b.WriteString(reverse + cellText(head, w) + reset)
		case r-1 < len(rows):
			b.WriteString(cellText(rows[r-1], w))
		default:
			b.WriteString(strings.Repeat(" ", w))
		}
		b.WriteString(sep)
	}
}

// bar is the status line below the grid.
func (ps *panes) bar() string {
	n := len(ps.list)
	what, keys := fmt.Sprintf("%d subagents", n), fmt.Sprintf("1-%d", min(n, 9))
	if n == 1 {
		what, keys = "1 subagent", "1"
	}
	return fmt.Sprintf(" %s   %s zoom  0 grid  q detach  ctrl+c stop", what, keys)
}

// key handles what was typed while the layout is shown and reports whether
// to leave it. Ctrl+C is no key of the layout: it goes on to the shell.
func (ps *panes) key(in []byte) (detach bool) {
	for len(in) > 0 {
		c := in[0]
		in = in[1:]
		switch {
		case c >= '1' && c <= '9':
			if i := int(c - '1'); i < len(ps.list) {
				ps.zoom = i
			}
		case c == '0':
			ps.zoom = -1
		case c == 'q' || c == ctrlO:
			return true
		case c != 0x1b:
		case len(in) == 0 || in[0] == 0x1b:
			ps.zoom = -1 // Esc
		case in[0] == '[':
			// A CSI sequence, an arrow say: up to its final byte.
			i := 1
			for i < len(in) && (in[i] < 0x40 || in[i] > 0x7e) {
				i++
			}
			in = in[min(i+1, len(in)):]
		case in[0] == 'O':
			in = in[min(2, len(in)):] // SS3: the keypad's 1 is ESC O q
		default:
			in = in[1:] // Alt with a key
		}
	}
	return false
}

// holding reports whether the alternate screen is taken, by the viewer or
// by the panes: the output waits in p.held meanwhile.
func (p *Proxy) holding() bool { return p.view != nil || (p.panes != nil && p.panes.shown) }

// paneKey handles what was typed while the layout is shown. Ctrl+C goes on
// to the shell, which stops the request and the subagents with it; the
// rest is the layout's. Called under p.mu.
func (p *Proxy) paneKey(b []byte) []byte {
	var keys, pass []byte
	for _, c := range b {
		if c == 0x03 {
			pass = append(pass, c)
		} else {
			keys = append(keys, c)
		}
	}
	if p.panes.key(keys) {
		p.detachPanes()
	} else if len(keys) > 0 {
		p.drawPanes()
	}
	return pass
}

// panesDue opens the layout of ps once paneDelay is over, unless Ctrl+O
// opened it sooner or the call closed it. Called under p.mu.
func (p *Proxy) panesDue(ps *panes) {
	if p.panes == ps && ps.delay != nil && p.view == nil {
		p.showPanes()
	}
}

// showPanes puts the layout on the alternate screen. Called under p.mu.
func (p *Proxy) showPanes() {
	ps := p.panes
	if ps.delay != nil {
		ps.delay.Stop() // shown sooner, by Ctrl+O: q then is not undone
		ps.delay = nil
	}
	if p.size == nil {
		return
	}
	ps.shown = true
	ps.resize(p.size())
	_, _ = p.out.Write([]byte(panesOpen))
	p.drawPanes()
}

// drawPanes draws the layout shown. Called under p.mu.
func (p *Proxy) drawPanes() {
	p.panes.last = time.Now()
	_, _ = p.out.Write(p.panes.render())
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
func (p *Proxy) detachPanes() {
	p.panes.shown = false
	_, _ = p.out.Write([]byte(panesClose))
	_, _ = p.out.Write(p.held)
	p.held = nil
}

// closePanes ends the layout: the screen comes back with what it held, a
// line per pane sums it up below the call, and its output goes to the
// folds. Once the request is over (a second Ctrl+C let the shell go back
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
		if text != "" {
			p.folds = append(p.folds, Fold{Title: pn.title, Text: text})
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
