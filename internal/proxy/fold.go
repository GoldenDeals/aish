package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

	// at is set when the status goes to the right of the command, which the
	// agent printed without a newline.
	at    *statusAt
	drawn bool // the status is on the screen at at.col
}

// statusAt is where the command printed by the agent ends on the screen.
type statusAt struct {
	col, cols int
	long      bool // the command takes several lines: the status is short
	hidden    int  // lines of the command the agent did not print
}

func newFold(title string, limit int) *fold {
	return &fold{
		title: title, limit: limit, hidden: limit == 0,
		raw:  capture.NewBuffer(foldRawCap, foldRawCap),
		rest: capture.NewBuffer(foldRawCap, foldRawCap),
	}
}

// write returns what of b goes to the terminal.
func (f *fold) write(b []byte) []byte {
	f.raw.Write(b)
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

// statusExit draws the status; exit < 0 while the command runs.
func (f *fold) statusExit(exit int) string {
	if f.at == nil {
		return "\r\x1b[K\x1b[0m" + dim + "  (" + strings.Join(f.parts(exit, true), " · ") + ")" + reset
	}
	full := "  (" + strings.Join(f.parts(exit, true), " · ") + ")"
	short := "  (" + strings.Join(f.parts(exit, false), " · ") + ")"
	room := f.at.cols - f.at.col
	var b strings.Builder
	if !f.drawn && utf8.RuneCountInString(short) > room {
		// No room after the command: the status goes below it.
		b.WriteString("\r\n")
		f.at.col, room = 0, f.at.cols
	}
	f.drawn = true
	text := short
	if !f.at.long && utf8.RuneCountInString(full) <= room {
		text = full
	}
	// The status is redrawn in place: autowrap stays off so it never moves
	// the cursor to another line.
	b.WriteString("\r")
	if f.at.col > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", f.at.col)
	}
	b.WriteString("\x1b[K\x1b[?7l\x1b[0m" + dim + text + reset + "\x1b[?7h")
	return b.String()
}

// parts are the items of the status; hint adds "ctrl+o to expand".
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
	if n > 0 && hint {
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
// otherwise if anything stayed hidden. exit < 0 when unknown.
func (f *fold) finish(exit int) []byte {
	if f.open || !f.folded() && f.limit > 0 {
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
