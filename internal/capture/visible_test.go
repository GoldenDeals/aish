package capture

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

func TestVisible(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ls -l /tmp", "ls -l /tmp"},
		{"rm -rf ~/proj #\x1b[2K\r\x1b[36m❯\x1b[39m ls", "rm -rf ~/proj #␛[2K␍␛[36m❯␛[39m ls"},
		{"a\x07b\x08c\x00d\x7fe", "a␇b␈c␀d␡e"},
		{"one\ntwo", "one\ntwo"},
		{"\u009b2Kx", "�2Kx"},                  // CSI of C1
		{"a\x9bb", "a�b"},                      // no UTF-8: C1 to a terminal that is not in UTF-8
		{"\u202esl\u2066x\u200fy", "�sl�x�y"},  // bidi controls reorder what follows
		{"日本\u200dz", "日本\u200dz"},             // a joiner reorders nothing
		{"a\tb\n\tc", "a       b\n        c"},  // tabs from each line's start
		{"日\tx", "日      x"},                   // wide: two columns before the tab
		{"\x1b[1mbold\x1b[0m", "␛[1mbold␛[0m"}, // styles too: the text is no caller's
	} {
		if got := Visible(c.in); got != c.want {
			t.Errorf("Visible(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := VisibleLine("a\nb\r\n"); got != "a␊b␍␊" {
		t.Errorf("VisibleLine: %q", got)
	}
}

// A question keeps its styles; nothing else of a terminal's sequences.
func TestVisibleStyled(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"\x1b[1mallow?\x1b[0m", "\x1b[1mallow?\x1b[0m"},
		{"\x1b[0m\x1b[1;4maish\x1b[0m: x", "\x1b[0m\x1b[1;4maish\x1b[0m: x"},
		{"\x1b[1mok\x1b[1A\x1b[2Kgone\x1b[0m", "\x1b[1mok␛[1A␛[2Kgone\x1b[0m"},
		{"\x1b[38:5:1mx\x1b[m", "\x1b[38:5:1mx\x1b[m"},
		{"\x1b[?25l\x1b[", "␛[?25l␛["},
		{"\x1b]0;title\a", "␛]0;title␇"},
		{"\x1b[1mab\tc", "\x1b[1mab      c"}, // the style takes no column
	} {
		if got := VisibleStyled(c.in); got != c.want {
			t.Errorf("VisibleStyled(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Whatever the bytes, nothing a terminal takes for a control is left, and
// each character takes the columns it is counted for.
func TestVisibleNoControls(t *testing.T) {
	var all []byte
	for c := 0; c < 256; c++ {
		all = append(all, byte(c), 'x')
	}
	s := string(all) + "\u0085\u009b\u009d\u202a\u202e\u2066\u2069\u061c\u200e"
	for name, got := range map[string]string{"Visible": Visible(s), "VisibleLine": VisibleLine(s)} {
		if !utf8.ValidString(got) {
			t.Errorf("%s: not UTF-8: %q", name, got)
		}
		for _, r := range got {
			if (r < 0x20 && (r != '\n' || name == "VisibleLine")) || (r >= 0x7f && r < 0xa0) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				t.Errorf("%s: %U left in %q", name, r, got)
			}
		}
		if got != Visible(got) && name == "Visible" {
			t.Errorf("%s: not as it was the second time", name)
		}
	}
	line := VisibleLine(s)
	n := 0
	for _, r := range line {
		n += runewidth.RuneWidth(r)
	}
	if w := runewidth.StringWidth(line); w != n || w < strings.Count(line, "x") {
		t.Errorf("width %d, by rune %d", w, n)
	}
}
