package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/capture"
)

// viewer shows folded outputs in full on the alternate screen. Ctrl+O opens
// it and closes it again, leaving the screen as it was. While it is open it
// shows what comes: viewFrame takes the folds anew and draws them if they
// changed, following their end unless the user scrolled up from it. A fold
// without a title is no output but a line between them: the request the
// calls below it were made for, a cleared screen (history.go).
type viewer struct {
	parts []viewPart // the folds shown, one under another
	rows  []string   // their rows, a blank one between two folds but under a line
	title []bool
	sep   []bool // the rows of a line between the outputs
	start []int  // the first row of every part
	top   int
	w, h  int

	// scrolled is set once the user moved the view: from then on it follows
	// the end of the folds only while the end is on the page.
	scrolled bool
	timer    *time.Timer // the next viewFrame; nil for a viewer of the tests
	yolo     bool        // the bar has the mark of aish yolo, see yolo.go
}

// viewPart is a fold as the viewer shows it: its lines, the title's first,
// and those wrapped to the width w. It is kept from one tick to the next,
// so that a fold that did not change is not cleaned and wrapped again.
type viewPart struct {
	fold   Fold
	sep    bool // a line between the outputs, all of it
	lines  []string
	titles int // how many of the lines are the title's
	w      int
	rows   []string
	trows  int // how many of the rows are the title's
}

func newViewer(folds []Fold, w, h int) *viewer {
	v := &viewer{w: max(w, 10), h: max(h, 2)}
	v.set(folds)
	if n := len(v.start); n > 0 {
		// The most recent output first, under its request's line if it is
		// the request's first.
		i := n - 1
		for i > 0 && v.parts[i-1].sep {
			i--
		}
		v.top = v.start[i]
	}
	v.clamp()
	return v
}

// newViewPart is f as the viewer shows it, not wrapped yet.
func newViewPart(f Fold) viewPart {
	pt := viewPart{fold: f, sep: f.Title == ""}
	if pt.sep {
		for _, l := range strings.Split(cleanText(f.Text), "\n") {
			pt.lines = append(pt.lines, expandTabs(l))
		}
		return pt
	}
	// A command's lines after the first are indented, as on the screen.
	for j, l := range strings.Split(f.Title, "\n") {
		if j > 0 {
			l = "  " + l
		}
		pt.lines = append(pt.lines, expandTabs(l))
	}
	pt.titles = len(pt.lines)
	text := cleanText(f.Text)
	if text == "" {
		return pt // a command cut short on the screen, with no output
	}
	for _, l := range strings.Split(text, "\n") {
		pt.lines = append(pt.lines, expandTabs(l))
	}
	return pt
}

// cleanText is capture.Clean of s, at no cost for a text with nothing to
// clean: the outputs of the journal were cleaned as they were recorded,
// and the viewer opens on all of them at once.
func cleanText(s string) string {
	if plainText(s) {
		return strings.TrimRight(s, "\n")
	}
	return capture.Clean([]byte(s))
}

// plainText reports whether capture.Clean leaves s as it is, but for the
// newlines at its end: valid UTF-8 with no control character other than a
// tab or a newline, and no line ending in a space.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\n' || c == '\t':
		case c < 0x20 || c == 0x7f:
			return false
		case c == ' ' && (i+1 == len(s) || s[i+1] == '\n'):
			return false
		}
	}
	return true
}

// wrap wraps the lines of pt to the width w.
func (pt *viewPart) wrap(w int) {
	pt.w, pt.rows, pt.trows = w, nil, 0
	for i, l := range pt.lines {
		rows := wrapWidth(l, w)
		if i < pt.titles {
			pt.trows += len(rows)
		}
		pt.rows = append(pt.rows, rows...)
	}
}

// expandTabs puts spaces for the tabs of a line, up to the next tab stop
// every 8 columns, as the terminal moves the cursor: ls lines up its
// columns with tabs, read_file ends the number of a line with one.
func expandTabs(l string) string {
	if !strings.ContainsRune(l, '\t') {
		return l
	}
	var b strings.Builder
	col := 0
	for _, r := range l {
		if r == '\t' {
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
	}
	return b.String()
}

// wrapWidth cuts l into rows at most w columns wide on the screen. A wide
// character that does not fit goes to the next row, as the terminal would
// put it: a row of the frame wider than the screen would wrap there and
// push the rest of the frame down.
func wrapWidth(l string, w int) []string {
	if printable(l) { // a column a byte
		rows := make([]string, 0, len(l)/w+1)
		for len(l) > w {
			rows, l = append(rows, l[:w]), l[w:]
		}
		return append(rows, l)
	}
	var rows []string
	start, col := 0, 0
	for i, r := range l {
		n := runewidth.RuneWidth(r)
		if col+n > w && i > start {
			rows = append(rows, l[start:i])
			start, col = i, 0
		}
		col += n
	}
	return append(rows, l[start:])
}

// printable reports whether l is printable ASCII only.
func printable(l string) bool {
	for i := 0; i < len(l); i++ {
		if l[i] < 0x20 || l[i] > 0x7e {
			return false
		}
	}
	return true
}

// set shows folds, laying out anew only those it did not show before in
// the same place, and the rows from the first of them on: the history of
// the session above them stays as it is.
func (v *viewer) set(folds []Fold) {
	parts := make([]viewPart, len(folds))
	from := min(len(folds), len(v.parts))
	for i, f := range folds {
		if i < len(v.parts) && v.parts[i].fold == f {
			parts[i] = v.parts[i]
		} else {
			parts[i] = newViewPart(f)
			from = min(from, i)
		}
	}
	v.parts = parts
	v.layoutFrom(from)
}

// shows reports whether v shows folds already.
func (v *viewer) shows(folds []Fold) bool {
	if len(folds) != len(v.parts) {
		return false
	}
	for i, f := range folds {
		if v.parts[i].fold != f {
			return false
		}
	}
	return true
}

// update shows folds in place of what v shows and reports whether they
// changed. Unless the user scrolled up from the end, the view follows it
// as a terminal does: the top moves only as far as the last row needs.
func (v *viewer) update(folds []Fold) bool {
	if v.shows(folds) {
		return false
	}
	follow := !v.scrolled || v.atEnd()
	v.set(folds)
	if follow {
		v.top = max(v.top, len(v.rows)-v.page())
	}
	v.clamp()
	return true
}

// layout puts the rows of the parts, wrapped to the width, one under
// another.
func (v *viewer) layout() { v.layoutFrom(0) }

// layoutFrom is layout from part from on, the rows of those before it left
// as the last layout put them.
func (v *viewer) layoutFrom(from int) {
	n := 0
	if from = min(from, len(v.start)); from > 0 {
		n = v.start[from-1] + len(v.parts[from-1].rows)
	}
	v.rows, v.title, v.sep, v.start = v.rows[:n], v.title[:n], v.sep[:n], v.start[:from]
	for i := from; i < len(v.parts); i++ {
		pt := &v.parts[i]
		if pt.w != v.w {
			pt.wrap(v.w)
		}
		if i > 0 && !v.parts[i-1].sep { // a line goes with what follows it
			v.rows = append(v.rows, "")
			v.title = append(v.title, false)
			v.sep = append(v.sep, false)
		}
		v.start = append(v.start, len(v.rows))
		v.rows = append(v.rows, pt.rows...)
		for j := range pt.rows {
			v.title = append(v.title, j < pt.trows)
			v.sep = append(v.sep, pt.sep)
		}
	}
	v.clamp()
}

// resize rewraps the lines to the terminal width. A view at the end stays
// there.
func (v *viewer) resize(w, h int) {
	end := v.atEnd()
	v.w, v.h = max(w, 10), max(h, 2)
	v.layout()
	if end {
		v.top = len(v.rows)
	}
	v.clamp()
}

func (v *viewer) page() int { return v.h - 1 }

func (v *viewer) clamp() {
	v.top = max(min(v.top, len(v.rows)-v.page()), 0)
}

// atEnd reports whether the last row is on the page.
func (v *viewer) atEnd() bool { return v.top >= len(v.rows)-v.page() }

func (v *viewer) open() []byte {
	return append([]byte("\x1b[?1049h\x1b[?25l"), v.render()...)
}

// close leaves the cursor hidden: a question or a form may keep it so, and
// closeView knows whether one is open.
func (v *viewer) close() []byte { return []byte("\x1b[?1049l") }

func (v *viewer) render() []byte {
	rows, title := v.rows, v.title
	var b bytes.Buffer
	b.WriteString("\x1b[H")
	for i := 0; i < v.page(); i++ {
		j := v.top + i
		if j < len(rows) {
			switch {
			case v.sep[j]:
				b.WriteString("\x1b[1m" + rows[j] + "\x1b[0m")
			case title[j]:
				b.WriteString("\x1b[1;36m" + rows[j] + "\x1b[0m")
			default:
				b.WriteString(rows[j])
			}
		}
		b.WriteString("\x1b[K\r\n")
	}
	end := min(v.top+v.page(), len(rows))
	bar := fmt.Sprintf(" %d-%d/%d  ↑↓ PgUp PgDn g G  ctrl+o/q close ", v.top+1, end, len(rows))
	mark, room := yoloBarMark(v.yolo, v.w)
	fmt.Fprintf(&b, "\x1b[7m%s\x1b[0m\x1b[K", runewidth.Truncate(bar, room, ""))
	if mark != "" {
		fmt.Fprintf(&b, "\x1b[%dG%s", room+1, mark)
	}
	return b.Bytes()
}

var viewerKeys = []struct {
	seq string
	act func(v *viewer) bool // reports close
}{
	{"\x1b[A", func(v *viewer) bool { v.top--; return false }},
	{"\x1bOA", func(v *viewer) bool { v.top--; return false }},
	{"\x1b[B", func(v *viewer) bool { v.top++; return false }},
	{"\x1bOB", func(v *viewer) bool { v.top++; return false }},
	{"\x1b[5~", func(v *viewer) bool { v.top -= v.page(); return false }},
	{"\x1b[6~", func(v *viewer) bool { v.top += v.page(); return false }},
	{"\x1b[H", func(v *viewer) bool { v.top = 0; return false }},
	{"\x1b[F", func(v *viewer) bool { v.top = 1 << 30; return false }},
	{"k", func(v *viewer) bool { v.top--; return false }},
	{"j", func(v *viewer) bool { v.top++; return false }},
	{"\r", func(v *viewer) bool { v.top++; return false }},
	{"b", func(v *viewer) bool { v.top -= v.page(); return false }},
	{" ", func(v *viewer) bool { v.top += v.page(); return false }},
	{"g", func(v *viewer) bool { v.top = 0; return false }},
	{"G", func(v *viewer) bool { v.top = 1 << 30; return false }},
	{"q", func(v *viewer) bool { return true }},
	{"\x0f", func(v *viewer) bool { return true }}, // Ctrl+O
	{"\x03", func(v *viewer) bool { return true }}, // Ctrl+C
}

// key handles input while the viewer is open and reports whether to close.
func (v *viewer) key(in []byte) (closed bool) {
	top := v.top
	for len(in) > 0 {
		matched := false
		for _, k := range viewerKeys {
			if bytes.HasPrefix(in, []byte(k.seq)) {
				if k.act(v) {
					return true
				}
				in = in[len(k.seq):]
				matched = true
				break
			}
		}
		switch {
		case matched:
		case bytes.HasPrefix(in, []byte("\x1b[")):
			// Unknown CSI sequence: skip up to its final byte.
			i := 2
			for i < len(in) && (in[i] < 0x40 || in[i] > 0x7e) {
				i++
			}
			in = in[min(i+1, len(in)):]
		case bytes.HasPrefix(in, []byte("\x1bO")):
			in = in[min(3, len(in)):]
		case in[0] == 0x1b:
			return true // Esc
		default:
			in = in[1:] // anything else is ignored
		}
	}
	v.clamp()
	if v.top != top {
		v.scrolled = true
	}
	return false
}

// viewTick is how often the open viewer looks for what came: a frame at
// most that often, and only when there is something new to show.
const viewTick = 200 * time.Millisecond

// viewFrame shows in v what came since its last frame, unless v was
// closed.
func (p *Proxy) viewFrame(v *viewer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.view != v {
		return
	}
	p.refreshView()
	v.timer.Reset(viewTick)
}

// refreshView draws the open viewer anew if the folds changed: the output
// of a running command grew, it ended, another came. Past p.held, which
// keeps the same output for the screen under the viewer. Called under p.mu.
func (p *Proxy) refreshView() {
	if p.view.update(p.viewFolds()) {
		p.write(p.view.render())
	}
}

// closeView shows the cursor the viewer hid, unless an open question or
// form keeps it hidden. What was held goes after it, so that a spinner
// drawn meanwhile hides it again.
func (t *console) closeView() {
	if t.view.timer != nil {
		t.view.timer.Stop()
	}
	cursor := "\x1b[?25h"
	if t.ask != nil || t.form != nil {
		cursor = "\x1b[?25l"
	}
	t.write(append(t.view.close(), cursor...))
	t.write(t.held)
	t.view, t.held = nil, nil
	t.syncPaste() // after what was held: the mode the shell set there is in it
}
