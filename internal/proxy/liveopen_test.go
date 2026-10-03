package proxy

import (
	"slices"
	"strings"
	"testing"
)

// Without folding (fold_lines = -1) an external tool's output passes
// through; Finish ends the last line it left open, so the spinner of the
// next turn starts below it. A line already ended gets no empty line.
func TestLiveUnfoldedOpenLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writes []string
		end    string // what Finish prints
		screen []string
	}{
		{"open", []string{"a\nb"}, "\r\n", []string{"", "a", "b", "⠋ thinking…"}},
		{"ended", []string{"a\nb\n"}, "", []string{"", "a", "b", "⠋ thinking…"}},
		{"open at last write", []string{"a\n", "b"}, "\r\n", []string{"", "a", "b", "⠋ thinking…"}},
		{"colours reset after the end", []string{"a\nb\n\x1b[m"}, "", []string{"", "a", "b", "⠋ thinking…"}},
		{"line erased", []string{"a\n50%\r\x1b[K"}, "", []string{"", "a", "⠋ thinking…"}},
		{"no output", nil, "", []string{"", "⠋ thinking…"}},
	} {
		p, out := statusProxy(t, 80)
		p.foldLines = -1
		u := &ui{p: p}
		u.CommandAt(9, false, 0)
		l := u.Live("⚙ probe")
		for _, w := range tc.writes {
			l.Write([]byte(w))
		}
		before := out.String()
		l.Finish(0)
		if end := strings.TrimPrefix(out.String(), before); end != tc.end {
			t.Errorf("%s: Finish printed %q, want %q", tc.name, end, tc.end)
		}
		l.Finish(0) // once ended, the line stays ended
		u.Write([]byte(spinnerFrame))
		if got := screenRows(out.String()); !slices.Equal(got, tc.screen) {
			t.Errorf("%s: screen %q, want %q", tc.name, got, tc.screen)
		}
	}
}

// A cleared screen leaves no text on the cursor's line, wherever the
// cursor stayed: `clear` after an unended line needs no newline.
func TestLastLineClear(t *testing.T) {
	for _, tc := range []struct {
		out  string
		text bool
	}{
		{"x\x1b[2J", false},
		{"x\x1b[H\x1b[2J", false},
		{"x\x1b[H\x1b[2J\x1b[3J", false},
		{"x\x1b[2Jy", true},
		{"x\r\x1b[J", false},
		{"x\x1b[J", true},
		{"x\x1b[3J", true},
		{"x\x1b[H\x1b[J", false},
		{"x\x1b[?1049h\x1b[2J", true},
	} {
		var l lastLine
		l.feed([]byte(tc.out))
		if l.text != tc.text {
			t.Errorf("%q: text %v, want %v", tc.out, l.text, tc.text)
		}
	}
}
