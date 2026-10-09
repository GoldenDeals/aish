package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

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
		mark, room := yoloBarMark(ps.yolo, ps.w)
		fmt.Fprintf(&b, "\x1b[%d;1H%s%s%s%s", ps.h, reverse, cellText(ps.bar(), room), reset, mark)
	}
	b.WriteString("\x1b[?7h")
	return b.Bytes()
}

// drawPane draws pane i in c: its header, then the tail of its output. A
// pane zoomed has the keys in its header, and the mark of aish yolo, as
// there is no status bar then.
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
	mark, room := "", w
	if ps.zoom == i {
		mark, room = yoloBarMark(ps.yolo, w)
	}
	rows := pn.rows(w, c.h-1)
	for r := range c.h {
		fmt.Fprintf(b, "\x1b[%d;%dH", c.y+r+1, c.x+1)
		switch {
		case r == 0:
			b.WriteString(reverse + cellText(head, room) + reset + mark)
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
