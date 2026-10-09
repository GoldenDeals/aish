package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/capture"
)

// viewer shows folded outputs in full on the alternate screen. Ctrl+O opens
// it and closes it again, leaving the screen as it was. While it is open it
// shows what comes: viewFrame takes the folds anew and draws them if they
// changed, following their end unless the user scrolled up from it.
type viewer struct {
	parts []viewPart // the folds shown, one under another
	rows  []string   // their rows, a blank one between two folds
	title []bool
	start []int // the first row of every part
	top   int
	w, h  int

	// scrolled is set once the user moved the view: from then on it follows
	// the end of the folds only while the end is on the page.
	scrolled bool
	timer    *time.Timer // the next viewFrame; nil for a viewer of the tests
}

// viewPart is a fold as the viewer shows it: its lines, the title's first,
// and those wrapped to the width w. It is kept from one tick to the next,
// so that a fold that did not change is not cleaned and wrapped again.
type viewPart struct {
	fold   Fold
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
		v.top = v.start[n-1] // the most recent output first
	}
	v.clamp()
	return v
}

// newViewPart is f as the viewer shows it, not wrapped yet.
func newViewPart(f Fold) viewPart {
	pt := viewPart{fold: f}
	// A command's lines after the first are indented, as on the screen.
	for j, l := range strings.Split(f.Title, "\n") {
		if j > 0 {
			l = "  " + l
		}
		pt.lines = append(pt.lines, expandTabs(l))
	}
	pt.titles = len(pt.lines)
	text := strings.TrimRight(capture.Clean([]byte(f.Text)), "\n")
	if text == "" {
		return pt // a command cut short on the screen, with no output
	}
	for _, l := range strings.Split(text, "\n") {
		pt.lines = append(pt.lines, expandTabs(l))
	}
	return pt
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

// set shows folds, laying out anew only those it did not show before in
// the same place.
func (v *viewer) set(folds []Fold) {
	parts := make([]viewPart, len(folds))
	for i, f := range folds {
		if i < len(v.parts) && v.parts[i].fold == f {
			parts[i] = v.parts[i]
		} else {
			parts[i] = newViewPart(f)
		}
	}
	v.parts = parts
	v.layout()
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
func (v *viewer) layout() {
	v.rows, v.title, v.start = nil, nil, nil
	for i := range v.parts {
		pt := &v.parts[i]
		if pt.w != v.w {
			pt.wrap(v.w)
		}
		if i > 0 {
			v.rows = append(v.rows, "")
			v.title = append(v.title, false)
		}
		v.start = append(v.start, len(v.rows))
		v.rows = append(v.rows, pt.rows...)
		for j := range pt.rows {
			v.title = append(v.title, j < pt.trows)
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
			if title[j] {
				b.WriteString("\x1b[1;36m" + rows[j] + "\x1b[0m")
			} else {
				b.WriteString(rows[j])
			}
		}
		b.WriteString("\x1b[K\r\n")
	}
	end := min(v.top+v.page(), len(rows))
	fmt.Fprintf(&b, "\x1b[7m %d-%d/%d  ↑↓ PgUp PgDn g G  ctrl+o/q close \x1b[0m\x1b[K", v.top+1, end, len(rows))
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

// viewFolds are the outputs of the current or last request, including the
// one being printed. The open viewer takes them anew on every tick.
func (r *recorder) viewFolds() []Fold {
	folds := append([]Fold{}, r.folds...)
	if f := r.liveFold(); f != nil && !f.open {
		// A command cut short on the screen is there before it prints anything.
		if raw := f.raw.Bytes(); len(raw) > 0 || f.cut() {
			folds = append(folds, Fold{Title: f.title + "  (running)", Text: string(raw)})
		}
	}
	return folds
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
