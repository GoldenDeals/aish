package capture

import (
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// screenText writes stream to a text buffer n bytes at a time, as a PTY
// hands it over, and returns what Screen reads of it on a terminal cols
// wide.
func screenText(stream string, n, cols int) string {
	b := NewText(64<<10, 64<<10)
	for len(stream) > n {
		b.Write([]byte(stream[:n]))
		stream = stream[n:]
	}
	b.Write([]byte(stream))
	return b.Screen(cols)
}

// bash -i recorded in a PTY 40 columns wide: a line longer than that
// edited (words inserted in its first row, a word erased at its end),
// found with Ctrl+R and changed, found and run again; a line as long as
// the row; a line of three rows with characters deleted in its first;
// completion with its list. Each prompt is the line as entered, the output
// below it, as the terminal showed them (tmux, its lines joined).
func TestScreenRecordedBash(t *testing.T) {
	raw, err := os.ReadFile("testdata/bash-40-columns.raw")
	if err != nil {
		t.Fatal(err)
	}
	xs := strings.Repeat("x", 77)
	want := strings.Join([]string{
		"user@remote:~$ cd dir",
		"user@remote:~/dir$ echo the quick red brown fox jumps over the lazy cat",
		"the quick red brown fox jumps over the lazy cat",
		"user@remote:~/dir$ echo the quick red brown fox jumps over the lazy cat now",
		"the quick red brown fox jumps over the lazy cat now",
		"user@remote:~/dir$ echo the quick red brown fox jumps over the lazy cat now",
		"the quick red brown fox jumps over the lazy cat now",
		"user@remote:~/dir$ echo 12345678901234567890",
		"12345678901234567890",
		"user@remote:~/dir$ echo " + xs,
		xs,
		"user@remote:~/dir$ ls -d sub",
		"sub1/           subdir-delta/",
		"sub2/           subdir-epsilon/",
		"subdir-alpha/   subdir-gamma/",
		"subdir-beta/",
		"user@remote:~/dir$ ls -d subdir-alpha/",
		"subdir-alpha/",
		"user@remote:~/dir$ exit",
		"exit",
	}, "\n")
	for _, n := range []int{len(raw), 1, 3, 7, 64} {
		if got := screenText(string(raw), n, 40); got != want {
			t.Errorf("written %d bytes at a time:\n%s\nwant:\n%s", n, got, want)
		}
	}
}

// The same of zsh: completion with menu selection, its list below the line
// erased once a match is taken; a long line edited; a search shown below
// it.
func TestScreenRecordedZsh(t *testing.T) {
	raw, err := os.ReadFile("testdata/zsh-40-columns.raw")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"user@remote:~% cd dir",
		"user@remote:~/dir% ls -d sub2",
		"sub2",
		"user@remote:~/dir% echo the quick red brown fox jumps over the lazy cat",
		"the quick red brown fox jumps over the lazy cat",
		"user@remote:~/dir% echo the quick red brown fox jumps over the lazy cat now",
		"the quick red brown fox jumps over the lazy cat now",
		"user@remote:~/dir% exit",
	}, "\n")
	for _, n := range []int{len(raw), 1, 5} {
		if got := screenText(string(raw), n, 40); got != want {
			t.Errorf("written %d bytes at a time:\n%s\nwant:\n%s", n, got, want)
		}
	}
}

// The sessions recorded 80 columns wide, on such a terminal, read as Clean
// reads them: nothing in them crossed rows.
func TestScreenRecordedAsClean(t *testing.T) {
	for _, name := range []string{"testdata/bash-session.raw", "testdata/zsh-session.raw"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := screenText(string(raw), 5, 80), Clean(raw); got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
	}
}

// A stream that never goes to another row but by a new line, on a
// terminal of any width or wider than its lines, reads as Clean reads it.
func TestCleanScreenAsClean(t *testing.T) {
	for _, in := range []string{
		"\x1b[1;32mok\x1b[0m\r\n",
		"progress 10%\rprogress 100%\r\ndone\r\n",
		"abc\b\bX\r\n",
		"\x1b]0;title\a\x1b(Bhello  \r\n",
		"a\r\n\r\nb",
		"$ echo helo world\b\b\b\b\b\b\b\x1b[1@l\r\n",
		"abc\b\b\x1b[3@X\n",
		"abc\x1b[2@X\n",
		"abcdef\r\x1b[2C\x1b[2P\n",
		"abcdef\r\x1b[2C\x1b[99Px\n",
		"abcdef\r\x1b[1C\x1b[3X\n",
		"long text\r\x1b[0;5Kshort\n",
		"long text\x1b[4D\x1b[K!\n",
		"abc\x1b[2Kx",
		"abcdef\r\x1b[3C\x1b[1Kx",
		"abc\r\x1b[3Kx\n",
		"abc\r\x1b[?2Kx\n",
		"a\n\rb\r\x1b[K",
		"abcdef\x1b[4GX\n",
		"Starting\x1b[12G[ OK ]\n",
		"ab\x1b[9DX\n",
		"ab\x1b[1;2CX\n",
		"ab\x1b[1AX\n",
		"Downloading\n\r\x1b[K  10% [##        ]\r\x1b[2K\x1b[1G 100% [##########]\ndone\n",
		"a\tb\r\n",
		sessionStream,
		"\x1b[?1049h\x1b[Hfile\r\n~\r\n\x1b[?1049l",
		"$ top\r\n\x1b[?1049h\x1b[Htop - 12:00:00 up 1 day\r\n",
		"a\r\n\x1b[?47hx\x1b[?47lb\r\n\x1b[?1047hy\x1b[?1047lc",
		strings.Repeat("\x1b[?1049hx\x1b[?1049l", 100) + "done\r\n",
		"\x1b[?1049hx\x1b[?1049l\r\n\r\n\x1b[?1049hy\x1b[?1049l",
		"\x1b[?1049hx\x1b[?1049l$ ls\r\n\x1b[?1049hy\x1b[?1049l",
		"abc\x1b[?1049hx\x1b[?1049ldef\r\n",
		"a\x1b[?1049lb",
	} {
		want := Clean([]byte(in))
		for _, cols := range []int{0, 80} {
			if got := CleanScreen([]byte(in), cols); got != want {
				t.Errorf("CleanScreen(%q, %d) = %q, want %q", in, cols, got, want)
			}
		}
	}
	gap := strings.Repeat(" ", farCol)
	if got, want := CleanScreen([]byte("a\x1b[99999Gb\x1b[99999Cc\n"), 0), "a"+gap+"b"+gap+"c"; got != want {
		t.Errorf("far columns: %d bytes, want %d", len(got), len(want))
	}
}

// Lines wrap at the terminal's width and read as one line; the cursor
// waits past the last column for the next character, as in xterm and tmux.
func TestCleanScreenWraps(t *testing.T) {
	for _, c := range []struct {
		in   string
		cols int
		want string
	}{
		{"0123456789abc\r\n", 10, "0123456789abc"},
		{"0123456789\r\nx", 10, "0123456789\nx"}, // as long as the row: no line under it
		{"0123456789\x1b[K\r\n", 10, "0123456789"},
		{"0123456789\bX", 10, "012345678X"},
		{"0123456789\x1b[1Kx\r\n", 10, "x"},
		{"top\r\n0123456789\x1b[Ax\r\n\r\n", 10, "top      x\n0123456789"}, // moved, it is in the last column
		{"0123456789\x1b[Cx\r\n", 10, "012345678x"},
		{"ab\x1b[99Cc\r\n", 10, "ab       c"},
		{"ab\x1b[99Gc\r\n", 10, "ab       c"},
		// Wide characters: one that does not fit in the row goes on in the
		// next; one cut in half is gone.
		{"あいう\r\n", 5, "あいう"},
		{"abcdあ\r\n", 5, "abcdあ"},
		{"あい\r\x1b[CX\r\n", 10, " Xい"},
		{"e\u0301te\u0301\r\n", 10, "e\u0301te\u0301"},
		// A tab past the end of the row stays one; over text it only moves.
		{"ab\tc\r\n", 20, "ab\tc"},
		{"abcdefghij\r\tY\r\n", 20, "abcdefghYj"},
		{"abcdefgh\t\tX\r\n", 10, "abcdefgh\tX"},
		// Without autowrap the last column is written over.
		{"\x1b[?7l0123456789abc\x1b[?7h\r\n", 10, "012345678c"},
	} {
		if got := CleanScreen([]byte(c.in), c.cols); got != c.want {
			t.Errorf("CleanScreen(%q, %d) = %q, want %q", c.in, c.cols, got, c.want)
		}
	}
}

// The cursor goes up and down the rows printed and draws over them.
func TestCleanScreenRows(t *testing.T) {
	for _, c := range []struct {
		name, in string
		cols     int
		want     string
	}{
		{"redrawn", "one\r\ntwo\r\n\x1b[2A\rONE\r\n", 20, "ONE\ntwo"},
		{"progress of several lines",
			"Pulling\r\na: 10%\r\nb: 20%\r\n" +
				"\x1b[2A\x1b[2K\ra: 50%\r\n\x1b[2K\rb: 60%\r\n" +
				"\x1b[2A\x1b[2K\ra: done\r\n\x1b[2K\rb: done\r\nok\r\n",
			40, "Pulling\na: done\nb: done\nok"},
		{"list below the line erased", "$ ls s\r\nsub1  sub2\x1b[A\r\x1b[6C\x1b[J\r\nsub1  sub2\r\n", 40, "$ ls s\nsub1  sub2"},
		{"a line of two rows redrawn", "$ echo 0123456789\x1b[A\r$ echo 01234567\x1b[K\r\n\x1b[K\x1b[A\r\n", 10, "$ echo 01234567"},
		{"up past the first row", "x\r\n\x1b[9Ay", 10, "y"},
		{"up past the first row, at once", "\x1b[5Aabc", 10, "abc"},
		{"down past the last row", "a\x1b[5Bb\r\n", 10, "ab"},
		{"next and previous line", "one\r\ntwo\x1b[Fx\x1b[Ey\r\n", 10, "xne\nywo"},
		{"reverse index", "one\r\ntwo\x1bMx\r\n", 10, "onex\ntwo"},
		{"saved and restored", "top\r\nab\x1b7\r\nmid\x1b8Z\r\n", 10, "top\nabZ\nmid"},
		{"saved and restored, CSI", "top\r\nab\x1b[s\r\nmid\x1b[uZ\r\n", 10, "top\nabZ\nmid"},
		{"rows inserted", "top\r\nline1\r\nline2\r\n\x1b[2A\x1b[Lnew\r\n\x1b[Bend\r\n", 10, "top\nnew\nline1\nende2"},
		{"rows deleted", "top\r\nline1\r\nline2\r\nline3\r\n\x1b[3A\x1b[Mx\r\n", 10, "top\nxine2\nline3"},
		// A row erased to its end no longer wraps (xterm); nor does the row
		// above one erased whole (tmux).
		{"erased to its end", "0123456789abc\x1b[A\r\x1b[2C\x1b[K\r\n\r\n", 10, "01\nabc"},
		{"the row under erased", "0123456789abc\r\x1b[KY\r\n", 10, "0123456789\nY"},
		{"the row under erased whole", "0123456789abc\x1b[2KY\r\n", 10, "0123456789\n   Y"},
		{"the row under erased in part", "0123456789abc\r\x1b[1CY\x1b[K\r\n", 10, "0123456789aY"},
		{"deleted in a wrapped row", "0123456789abc\x1b[A\r\x1b[2P\r\n\r\n", 10, "23456789  abc"},
		{"inserted in a wrapped row", "0123456789abc\x1b[A\r\x1b[2@\r\n\r\n", 10, "  01234567abc"},
		// Leaving the alternate screen, the cursor is below the line of the
		// program: what comes next does not go over it.
		{"after a full-screen program", "a\r\n\x1b[?1049hx\x1b[?1049l\x1b[5Ab\r\n", 10, "a\n" + FullScreen + "\nb"},
	} {
		if got := CleanScreen([]byte(c.in), c.cols); got != c.want {
			t.Errorf("%s: CleanScreen(%q, %d) = %q, want %q", c.name, c.in, c.cols, got, c.want)
		}
	}
}

// The tail of a buffer begins anywhere: in the middle of a line, of a
// sequence, of a redraw that goes up rows it no longer has. It begins a
// screen of its own, under the omission, and the head stays as it was.
func TestScreenTail(t *testing.T) {
	b := NewText(32, 32)
	b.Write([]byte("$ ssh remote\r\nWelcome\r\n"))
	for range 50 {
		b.Write([]byte("a line of the session\r\n"))
	}
	b.Write([]byte("\x1b[3A\r\x1b[Kredrawn\x1b[J\r\n$ "))
	got := b.Screen(40)
	head, tail, ok := strings.Cut(got, " bytes omitted ...]\n")
	if !strings.HasPrefix(got, "$ ssh remote\nWelcome\na line of\n[... ") || !ok {
		t.Fatalf("head and omission: %q", got)
	}
	if want := "redrawn\n$"; tail != want {
		t.Errorf("tail %q, want %q (head %q)", tail, want, head)
	}
	for _, c := range []struct{ in, want string }{
		{"5;1Hrest\r\n", "5;1Hrest"},
		{"\x1b[2A\x1b[5Dx\r\n", "x"},
		{"\x80\x81abc\x1b[3P", "\ufffd\ufffdabc"},
	} {
		if got := CleanScreen([]byte(c.in), 10); got != c.want {
			t.Errorf("CleanScreen(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Whatever the stream, the text is valid and bounded by it: no sequence
// grows what was printed more than a row's width at a time.
func TestCleanScreenBounded(t *testing.T) {
	seqs := []string{
		"a", "あ", "\u0301", "\t", "\r", "\n", "\b", "\x1b[A", "\x1b[3B", "\x1b[9C", "\x1b[D", "\x1b[99G",
		"\x1b[K", "\x1b[1K", "\x1b[2K", "\x1b[J", "\x1b[99@", "\x1b[5P", "\x1b[9X", "\x1b[99L", "\x1b[3M",
		"\x1b7", "\x1b8", "\x1bM", "\x1bD", "\x1b[?7l", "\x1b[?7h", "\x1b[?1049h", "\x1b[?1049l", "\x1b[", "\xff",
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		var in strings.Builder
		for range 300 {
			in.WriteString(seqs[r.IntN(len(seqs))])
		}
		for _, cols := range []int{0, 1, 2, 7, 40} {
			out := CleanScreen([]byte(in.String()), cols)
			if !utf8.ValidString(out) {
				t.Fatalf("not UTF-8 of %q, %d columns", in.String(), cols)
			}
			if limit := (in.Len() + len(FullScreen)) * max(cols, farCol, 8); len(out) > limit {
				t.Fatalf("%d bytes of %d, %d columns", len(out), in.Len(), cols)
			}
		}
	}
	marks := "e" + strings.Repeat("\u0301", 100000)
	if out := CleanScreen([]byte(marks), 40); len(out) > maxCell+4 {
		t.Errorf("combining marks: %d bytes", len(out))
	}
}

func FuzzCleanScreen(f *testing.F) {
	f.Add([]byte("0123456789abc\x1b[A\r\x1b[2@\r\n"), 10)
	f.Add([]byte("あい\x1b[?1049hx\x1b[?1049l\x1b[5A\t\x1b[L"), 3)
	f.Fuzz(func(t *testing.T, raw []byte, cols int) {
		if out := CleanScreen(raw, cols%300); !utf8.ValidString(out) {
			t.Fatalf("not UTF-8: %q", out)
		}
	})
}
