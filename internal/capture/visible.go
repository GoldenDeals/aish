package capture

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// Visible is s made safe to draw: text the model, a hook or a policy wrote
// that the user decides on, a call or the question about it. A control
// character (C0 but the newline, DEL, C1), a byte that is no UTF-8 and a
// bidi control would move the cursor, erase, restyle or reorder what is on
// the screen, and so show other text than s holds: the call over itself.
// Each shows as a sign one column wide: the Control Pictures symbol of a C0
// character or DEL (␛ for ESC, ␍ for \r), � for the rest, which have none.
// Not ^[ nor \e: a command has those as text often enough (a regex, a
// printf format), and the user could not tell the character from them. A
// tab goes as spaces to the next stop of 8 columns from the start of its
// line, so that what the callers count of the text is what it takes.
func Visible(s string) string { return visible(s, false, false) }

// VisibleLine is Visible of a text shown on one line, a newline as ␊.
func VisibleLine(s string) string { return visible(s, true, false) }

// VisibleStyled is Visible but keeps the SGR sequences (\e[…m) of s: the
// styles the caller drew a question with, which take no column and move
// nothing.
func VisibleStyled(s string) string { return visible(s, false, true) }

func visible(s string, line, styled bool) string {
	if plain(s, line) {
		return s
	}
	var b strings.Builder
	col := 0
	for i := 0; i < len(s); {
		if styled {
			if n := sgrLen(s[i:]); n > 0 {
				b.WriteString(s[i : i+n])
				i += n
				continue
			}
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == '\n' && !line:
			b.WriteByte('\n')
			col = 0
			continue
		case r == '\t':
			next := (col/8 + 1) * 8
			b.WriteString(strings.Repeat(" ", next-col))
			col = next
			continue
		case r < 0x20:
			r += 0x2400 // ␀ to ␟
		case r == 0x7f:
			r = '␡' // ␡
		case r >= 0x80 && r < 0xa0, unicode.Is(unicode.Bidi_Control, r):
			r = utf8.RuneError
		}
		// A byte that is no UTF-8 decodes as utf8.RuneError, and so shows.
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
	}
	return b.String()
}

// plain reports whether visible leaves s as it is: printable ASCII, with
// newlines unless line.
func plain(s string, line bool) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && (c != '\n' || line)) || c >= 0x7f {
			return false
		}
	}
	return true
}

// sgrLen is the length of the SGR sequence s starts with, 0 for none.
func sgrLen(s string) int {
	if !strings.HasPrefix(s, "\x1b[") {
		return 0
	}
	for i := 2; i < len(s); i++ {
		switch c := s[i]; {
		case c == 'm':
			return i + 1
		case c >= '0' && c <= '9', c == ';', c == ':':
		default:
			return 0
		}
	}
	return 0
}
