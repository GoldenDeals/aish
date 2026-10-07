package proxy

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

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

// clearAt removes the status drawn to the right of the command.
func (f *fold) clearAt() string {
	s := "\r"
	if f.at.col > 0 {
		s += fmt.Sprintf("\x1b[%dC", f.at.col)
	}
	return s + "\x1b[K"
}
