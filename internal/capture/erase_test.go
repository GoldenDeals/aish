package capture

import (
	"strings"
	"testing"
)

func TestCleanErasesInLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"long text\r\x1b[Kshort\n", "short"},
		{"long text\r\x1b[0Kshort\n", "short"},
		{"long text\r\x1b[0;5Kshort\n", "short"},
		{"long text\x1b[4D\x1b[K!\n", "long !"},
		{"abc\x1b[2Kx", "   x"},
		{"abc\x1b[2K\rx", "x"},
		{"abcdef\r\x1b[3C\x1b[1Kx", "   xef"},
		{"abcdef\x1b[1K\n", ""},
		{"abc\r\x1b[1K\n", " bc"},
		{"abc\r\x1b[3Kx\n", "xbc"},  // no such erase
		{"abc\r\x1b[?2Kx\n", "xbc"}, // private sequences are left alone
		{"a\n\rb\r\x1b[K", "a"},
	} {
		if got := Clean([]byte(c.in)); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanMovesInLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"abcdef\x1b[GX\n", "Xbcdef"},
		{"abcdef\x1b[1GX\n", "Xbcdef"},
		{"abcdef\x1b[0GX\n", "Xbcdef"},
		{"abcdef\x1b[4GX\n", "abcXef"},
		{"ab\x1b[6GX\n", "ab   X"},
		{"Starting\x1b[12G[ OK ]\n", "Starting   [ OK ]"},
		{"abcdef\x1b[DX\n", "abcdeX"},
		{"abcdef\x1b[3DX\n", "abcXef"},
		{"ab\x1b[9DX\n", "Xb"},
		{"abcdef\r\x1b[CX\n", "aXcdef"},
		{"abcdef\r\x1b[2CX\n", "abXdef"},
		{"ab\x1b[2CX\n", "ab  X"},
		{"ab\x1b[1;2CX\n", "ab X"},
		{"ab\x1b[1AX\n", "abX"}, // up a line: not followed
	} {
		if got := Clean([]byte(c.in)); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A progress bar the way curl, pip or cargo redraw it: what is left is its
// last frame and the lines around it.
func TestCleanProgress(t *testing.T) {
	in := "Downloading\n" +
		"\r\x1b[K  10% [##        ] 1.2 MB/s eta 0:00:09" +
		"\r\x1b[K  60% [######    ] 2.0 MB/s" +
		"\r\x1b[2K\x1b[1G 100% [##########]\n" +
		"done\n"
	want := "Downloading\n 100% [##########]\ndone"
	if got := Clean([]byte(in)); got != want {
		t.Errorf("Clean = %q, want %q", got, want)
	}
}

func TestCleanFarColumn(t *testing.T) {
	gap := strings.Repeat(" ", farCol)
	got := Clean([]byte("a\x1b[99999999999999999999Gb\x1b[99999Cc\n"))
	if want := "a" + gap + "b" + gap + "c"; got != want {
		t.Fatalf("%d bytes, want %d", len(got), len(want))
	}
	got = Clean([]byte(strings.Repeat("\x1b[9999C", 1000) + "x"))
	if want := gap + "x"; got != want {
		t.Fatalf("%d bytes, want %d", len(got), len(want))
	}
}
