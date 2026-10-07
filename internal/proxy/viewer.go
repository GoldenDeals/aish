package proxy

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/GoldenDeals/aish/internal/capture"
)

// viewer shows folded outputs in full on the alternate screen. Ctrl+O opens
// it and closes it again, leaving the screen as it was.
type viewer struct {
	lines  []string
	titles map[int]bool // indexes of title lines
	rows   []string     // lines wrapped to the width
	title  []bool
	first  []int // first row of every line
	top    int
	w, h   int
}

func newViewer(folds []Fold, w, h int) *viewer {
	v := &viewer{titles: map[int]bool{}}
	last := 0
	for i, f := range folds {
		if i > 0 {
			v.lines = append(v.lines, "")
		}
		last = len(v.lines)
		// A command's lines after the first are indented, as on the screen.
		for j, l := range strings.Split(f.Title, "\n") {
			if j > 0 {
				l = "  " + l
			}
			v.titles[len(v.lines)] = true
			v.lines = append(v.lines, strings.ReplaceAll(l, "\t", "        "))
		}
		text := strings.TrimRight(capture.Clean([]byte(f.Text)), "\n")
		if text == "" {
			continue // a command cut short on the screen, with no output
		}
		for _, l := range strings.Split(text, "\n") {
			v.lines = append(v.lines, strings.ReplaceAll(l, "\t", "        "))
		}
	}
	v.resize(w, h)
	v.top = v.first[last] // the most recent output first
	v.clamp()
	return v
}

// resize rewraps the lines to the terminal width.
func (v *viewer) resize(w, h int) {
	v.w, v.h = max(w, 10), max(h, 2)
	v.rows, v.title, v.first = nil, nil, nil
	for i, l := range v.lines {
		v.first = append(v.first, len(v.rows))
		r := []rune(l)
		for {
			n := min(len(r), v.w)
			v.rows = append(v.rows, string(r[:n]))
			v.title = append(v.title, v.titles[i])
			r = r[n:]
			if len(r) == 0 {
				break
			}
		}
	}
	v.clamp()
}

func (v *viewer) page() int { return v.h - 1 }

func (v *viewer) clamp() {
	v.top = max(min(v.top, len(v.rows)-v.page()), 0)
}

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
	return false
}
