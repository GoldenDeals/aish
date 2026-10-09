package capture

import "testing"

// Readline and zle insert and delete in the middle of the line they edit
// with ICH, DCH and ECH: the line left is the one entered.
func TestCleanEditsInLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"$ echo helo world\b\b\b\b\b\b\b\x1b[1@l\r\n", "$ echo hello world"},
		{"$ echo one; e\b\b\b\b\b\b\b\b\b\b\b\x1b[1@#\r\n", "$ #echo one; e"},
		{"abc\b\b\x1b[@X\n", "aXbc"},
		{"abc\b\b\x1b[3@X\n", "aX  bc"},
		{"abc\x1b[2@X\n", "abcX"}, // at the end of the line: nothing to move
		{"abcdef\r\x1b[2C\x1b[2P\n", "abef"},
		{"abcdef\r\x1b[2C\x1b[P\n", "abdef"},
		{"abcdef\r\x1b[2C\x1b[99Px\n", "abx"},
		{"abc\x1b[3P\n", "abc"},
		{"abcdef\r\x1b[1C\x1b[3X\n", "a   ef"},
		{"abcdef\r\x1b[4C\x1b[9X!\n", "abcd!"},
		{"abc\x1b[XZ\n", "abcZ"},
	} {
		if got := Clean([]byte(c.in)); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
