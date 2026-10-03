package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/capture"
)

const (
	dim   = "\x1b[2m"
	reset = "\x1b[0m"
)

const (
	foldRawCap    = 512 << 10 // head and tail of a folded output kept for Ctrl+O
	foldStatusGap = 100 * time.Millisecond
)

// fold shows the first lines of an agent command's output (none by default)
// and hides the rest behind a status line; the whole output is kept for
// Ctrl+O.
type fold struct {
	title  string
	limit  int // lines shown before folding
	lines  int
	hidden bool
	open   bool // expanded: everything passes through

	raw         *capture.Buffer // everything, for Ctrl+O at the prompt
	rest        *capture.Buffer // the hidden part, for Ctrl+O while running
	hiddenLines int
	partial     bool // the hidden part ends without a newline
	status      time.Time
	last        lastLine // the output's last line, which ends what is shown

	// at is set when the status goes on the line of the call, which the
	// agent printed without a newline.
	at    *statusAt
	drawn bool // the status is on the screen, after at.col
}

// statusAt is where the call printed by the agent ends on the screen; col
// is 0 once the status moved to a line of its own.
type statusAt struct {
	col, cols int
	hidden    int // lines of the command the agent did not print
}

func newFold(title string, limit int) *fold {
	return &fold{
		title: title, limit: limit, hidden: limit == 0,
		raw:  capture.NewBuffer(foldRawCap, foldRawCap),
		rest: capture.NewBuffer(foldRawCap, foldRawCap),
	}
}

// newResult is the fold of a built-in tool's result, given whole: only its
// status is shown, the lines counted as the agent counts them in a summary.
func newResult(title, text string) *fold {
	f := newFold(title, 0)
	s := strings.TrimSpace(text)
	f.hiddenLines, f.partial = strings.Count(s, "\n"), s != ""
	return f
}

// write returns what of b goes to the terminal.
func (f *fold) write(b []byte) []byte {
	f.raw.Write(b)
	f.last.feed(b)
	if f.open {
		return b
	}
	if hasAltScreen(b) {
		// A full-screen program owns the terminal; folding would break it.
		return append(f.expand(), b...)
	}
	var show []byte
	if !f.hidden {
		i := 0
		for ; i < len(b) && !f.hidden; i++ {
			if b[i] == '\n' {
				f.lines++
				f.hidden = f.lines >= f.limit
			}
		}
		if !f.hidden {
			return b
		}
		show, b = b[:i], b[i:]
	}
	if len(b) == 0 {
		return show
	}
	f.rest.Write(b)
	f.hiddenLines += bytes.Count(b, []byte{'\n'})
	f.partial = b[len(b)-1] != '\n'
	if time.Since(f.status) < foldStatusGap {
		return show
	}
	f.status = time.Now()
	return append(append([]byte{}, show...), f.statusExit(-1)...)
}

// folded reports whether anything was hidden.
func (f *fold) folded() bool { return f.hiddenLines > 0 || f.partial }

// cut reports whether the agent printed the command cut short: only the
// title, in Ctrl+O, shows it whole.
func (f *fold) cut() bool { return f.at != nil && f.at.hidden > 0 }

// keep reports whether the fold is worth keeping for Ctrl+O.
func (f *fold) keep() bool { return f.folded() || f.cut() }

// statusExit draws the status; exit < 0 while the command runs. On the
// line of the call it goes at the right edge, where the prompt's status
// does: in full if it fits between the call and the edge, short if only
// that does, and on a line of its own below the call otherwise.
func (f *fold) statusExit(exit int) string {
	full := "  (" + strings.Join(f.parts(exit, true), " · ") + ")"
	if f.at == nil {
		return "\r\x1b[K\x1b[0m" + dim + full + reset
	}
	short := "  (" + strings.Join(f.parts(exit, false), " · ") + ")"
	var b strings.Builder
	if f.at.col > 0 && !f.fits(short) {
		// The status outgrew the room it had: it moves below, for good.
		if f.drawn {
			b.WriteString(f.clearAt())
		}
		b.WriteString("\r\n")
		f.at.col = 0
	}
	f.drawn = true
	text := short
	if f.fits(full) {
		text = full
	}
	// The status is redrawn in place: autowrap stays off so it never moves
	// the cursor to another line.
	b.WriteString(f.clearAt() + "\x1b[?7l")
	if f.at.col > 0 {
		fmt.Fprintf(&b, "\x1b[%dG", max(f.at.col+1, f.at.cols-runewidth.StringWidth(text)))
	}
	b.WriteString("\x1b[0m" + dim + text + reset + "\x1b[?7h")
	return b.String()
}

// fits reports whether the status text fits where it is drawn: between
// the call and the right edge, whose last column stays free as with the
// prompt's status, or on a line of its own.
func (f *fold) fits(text string) bool {
	w := runewidth.StringWidth(text)
	if f.at.col == 0 {
		return w <= f.at.cols
	}
	return f.at.col+w < f.at.cols
}

// parts are the items of the status; hint adds "ctrl+o to expand" when
// there is something to expand: output, or a command cut short.
func (f *fold) parts(exit int, hint bool) []string {
	n := f.hiddenLines
	if f.partial {
		n++
	}
	var parts []string
	switch {
	case n == 0:
		parts = append(parts, "no output")
	case n == 1:
		parts = append(parts, "1 line")
	default:
		parts = append(parts, fmt.Sprintf("%d lines", n))
	}
	switch {
	case exit == 130:
		parts = append(parts, "interrupted")
	case exit > 0:
		parts = append(parts, fmt.Sprintf("exit %d", exit))
	}
	if hint && f.keep() {
		parts = append(parts, "ctrl+o to expand")
	}
	return parts
}

// expand stops folding and returns what is needed to show the hidden part.
func (f *fold) expand() []byte {
	if f.open {
		return nil
	}
	f.open = true
	var b []byte
	switch {
	case f.at == nil:
		b = []byte("\r\x1b[K")
	case f.drawn:
		b = []byte(f.clearAt() + "\r\n")
	default:
		b = []byte("\r\n") // the output starts below the command
	}
	if !f.folded() {
		if f.at == nil {
			return nil
		}
		return b
	}
	return append(b, f.rest.Bytes()...)
}

// finish returns the final status line: always when nothing is shown,
// otherwise if anything stayed hidden. exit < 0 when unknown. Output shown
// whole gets no status, but its last line is ended: what the agent prints
// next starts at the start of a line.
func (f *fold) finish(exit int) []byte {
	if f.open || !f.folded() && f.limit > 0 {
		if f.last.text {
			return []byte("\r\n")
		}
		return nil
	}
	return []byte(f.statusExit(exit) + "\r\n")
}

// clearAt removes the status drawn to the right of the command.
func (f *fold) clearAt() string {
	s := "\r"
	if f.at.col > 0 {
		s += fmt.Sprintf("\x1b[%dC", f.at.col)
	}
	return s + "\x1b[K"
}

func hasAltScreen(b []byte) bool {
	for _, s := range altScreenOn {
		if bytes.Contains(b, s) {
			return true
		}
	}
	return false
}

var altScreenOn = [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1047h"), []byte("\x1b[?47h")}
