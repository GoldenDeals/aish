package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/capture"
)

// paneSep divides the columns of the grid; where the locale makes the box
// line two columns wide it would break the layout.
var paneSep = func() string {
	if runewidth.StringWidth("│") == 1 {
		return "│"
	}
	return "|"
}()

// paneBrief is how many rows of its task a pane shows at most, under its
// header: a third of the pane's at most, the rest is the output's.
const paneBrief = 3

// pane is one subagent's output while it runs.
type pane struct {
	title  string
	prompt string          // the task it was given
	buf    *capture.Buffer // bounded: a chatty subagent must not grow forever
	tail   []byte          // the end of it from the start of a line: what the pane shows
	exit   int             // -1 while it runs
	done   bool
	// start is when it got its turn, zero while it is queued; end when it
	// was done.
	start, end time.Time
	calls      int  // the tool calls it made, told as it is done
	partial    bool // max_steps stopped it: its exit is 0, but no ok
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
	yolo bool // the bar has the mark of aish yolo, see yolo.go
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
	case !pn.done && pn.start.IsZero():
		return "queued"
	case !pn.done:
		return "running"
	case pn.exit == 0 && pn.partial:
		return "partial"
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

// brief is the first rows of the task, at most n of them w columns wide,
// its lines run together: what the pane is at, as its header says who. A
// task longer than that ends in "…"; Ctrl+O has it whole.
func (pn *pane) brief(w, n int) []string {
	text := strings.Join(strings.Fields(pn.prompt), " ")
	if w <= 0 || n <= 0 || text == "" {
		return nil
	}
	rows := wrap(text, w)
	if len(rows) > n {
		rows = rows[:n]
		rows[n-1] = runewidth.Truncate(rows[n-1]+" …", w, "…")
	}
	return rows
}

// foldTitle is the title of the pane's fold in Ctrl+O: its state follows,
// unless ok, as in its summary.
func (pn *pane) foldTitle() string {
	if s := pn.state(); s != "ok" {
		return pn.title + " (" + s + ")"
	}
	return pn.title
}

// fold is what Ctrl+O shows of the pane: its task, quoted, above its
// output text. "" when it has neither.
func (pn *pane) fold(text string) string {
	task := capture.Clean([]byte(agent.QuoteTask(pn.prompt)))
	switch {
	case task == "":
		return text
	case text == "":
		return task
	}
	return task + "\n\n" + text
}

// took is how long a pane ran, as a person writes it: 12s, 2m30s, 1h5m.
func took(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		d = d.Round(time.Minute)
	}
	s := d.String()
	if t, ok := strings.CutSuffix(s, "m0s"); ok {
		s = t + "m"
	}
	if t, ok := strings.CutSuffix(s, "h0m"); ok {
		s = t + "h"
	}
	return s
}

// summary is the line a pane leaves below the call once the layout is
// gone; text is its whole output. It tells how long the subagent ran and
// how many calls it made, unless it never started.
func (pn *pane) summary(text string) string {
	mark := paneFail + "✗" + reset
	switch pn.state() {
	case "ok":
		mark = paneOK + "✓" + reset
	case "partial":
		mark = panePartial + "!" + reset
	}
	var parts []string
	if !pn.start.IsZero() {
		end := pn.end
		if end.IsZero() {
			end = time.Now() // not done yet: a second Ctrl+C let the request go
		}
		parts = append(parts, took(end.Sub(pn.start)))
	}
	switch pn.calls {
	case 0:
	case 1:
		parts = append(parts, "1 call")
	default:
		parts = append(parts, fmt.Sprintf("%d calls", pn.calls))
	}
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
	if pn.fold(text) != "" {
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
		mark, room := yoloBarMark(ps.yolo, ps.w)
		fmt.Fprintf(&b, "\x1b[%d;1H%s%s%s%s", ps.h, reverse, cellText(ps.bar(), room), reset, mark)
	}
	b.WriteString("\x1b[?7h")
	return b.Bytes()
}

// drawPane draws pane i in c: its header, the first rows of its task dim,
// then the tail of its output. A pane zoomed has the keys in its header,
// and the mark of aish yolo, as there is no status bar then.
func (ps *panes) drawPane(b *bytes.Buffer, i int, c cell) {
	if c.w <= 0 || c.h <= 0 {
		return // a screen too small for all of them
	}
	pn := ps.list[i]
	w, sep := c.w, ""
	if c.x+c.w < ps.w {
		w, sep = c.w-1, dim+paneSep+reset
	}
	mark, room := "", w
	if ps.zoom == i {
		mark, room = yoloBarMark(ps.yolo, w)
	}
	head := paneHead(i, pn.title, pn.state(), room)
	if ps.zoom == i {
		head += "   0 grid  q detach  ctrl+c stop"
	}
	brief := pn.brief(w, min(paneBrief, (c.h-1)/3))
	rows := pn.rows(w, c.h-1-len(brief))
	for r := range c.h {
		fmt.Fprintf(b, "\x1b[%d;%dH", c.y+r+1, c.x+1)
		switch {
		case r == 0:
			b.WriteString(reverse + cellText(head, room) + reset + mark)
		case r-1 < len(brief):
			b.WriteString(dim + cellText(brief[r-1], w) + reset)
		case r-1-len(brief) < len(rows):
			b.WriteString(cellText(rows[r-1-len(brief)], w))
		default:
			b.WriteString(strings.Repeat(" ", w))
		}
		b.WriteString(sep)
	}
}

// paneHead is the header of pane i, room columns wide: its number, title
// and state. The title gives way, not the state: what changes.
func paneHead(i int, title, state string, room int) string {
	num := fmt.Sprintf(" %d ", i+1)
	if fit := room - runewidth.StringWidth(num) - 2 - runewidth.StringWidth(state); runewidth.StringWidth(title) > fit {
		title = runewidth.Truncate(title, max(fit, 1), "…")
	}
	return num + title + "  " + state
}

// bar is the status line below the grid: how many of the subagents run,
// wait their turn and are done.
func (ps *panes) bar() string {
	var running, queued, done int
	for _, pn := range ps.list {
		switch {
		case pn.done:
			done++
		case pn.start.IsZero():
			queued++
		default:
			running++
		}
	}
	var what []string
	for _, c := range []struct {
		n    int
		what string
	}{{running, "running"}, {queued, "queued"}, {done, "done"}} {
		if c.n > 0 {
			what = append(what, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	n := len(ps.list)
	keys := fmt.Sprintf("1-%d", min(n, 9))
	if n == 1 {
		keys = "1"
	}
	return fmt.Sprintf(" %s   %s zoom  0 grid  q detach  ctrl+c stop", strings.Join(what, " · "), keys)
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
