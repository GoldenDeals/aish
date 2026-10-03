package markdown

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestLineRows(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	for _, c := range []struct {
		s         string
		col, rows int
	}{
		{"", 0, 1},
		{a(20), 0, 1},
		{a(21), 0, 2},
		{a(18) + "中", 0, 1},
		{a(19) + "中", 0, 2},
		// The wide character leaves the last column of the first row empty.
		{a(19) + "中" + a(19), 0, 3},
		{a(19) + "中" + a(18) + "中", 0, 3},
		{"", 5, 1},
		{a(18), 2, 1},
		{a(19), 2, 2},
		{a(16) + "中", 2, 1},
		{a(17) + "中", 2, 2},
		{a(17) + "中" + a(19), 2, 3},
	} {
		if got := lineRows(c.s, c.col, 20); got != c.rows {
			t.Errorf("%q from column %d: %d rows, want %d", c.s, c.col, got, c.rows)
		}
	}
}

// TestEraseWide is TestEraseRaw with wide characters, which the terminal
// moves whole to the next row when they do not fit at the end of one, and
// with the line started after some text: the raw preview gives way to the
// rendering, and the text before the line stays. That text may be wider
// than the terminal and end at its last column, waiting to wrap.
func TestEraseWide(t *testing.T) {
	const cols, height = 20, 6
	words := []rune(strings.Repeat("lorem 中文 ipsum 漢字dolor sit 中amet ", 4))
	befores := map[string]string{
		"top":    "",
		"below":  "output\n",
		"bottom": strings.Repeat("output\n", height),
	}
	failed := map[string]bool{}
	for n := 1; runewidth.StringWidth(string(words[:n])) <= 3*cols; n++ {
		line := words[:n]
		for where, before := range befores {
			for _, start := range []int{0, 3, cols, cols + 2} {
				prefix := strings.Repeat("$", start)
				for _, chunk := range []int{1, 7, 0} {
					key := fmt.Sprintf("%s, from column %d, chunks of %d", where, start, chunk)
					if chunk == 0 {
						key, chunk = fmt.Sprintf("%s, from column %d, whole", where, start), n
					}
					if failed[key] {
						continue // one length is enough to tell
					}
					size := func() (int, int) { return cols, height }
					var out strings.Builder
					m := New(&out, size, "monokai")
					m.Col = start
					for i := 0; i < n; i += chunk {
						m.Write([]byte(string(line[i:min(i+chunk, n)])))
					}
					m.Write([]byte("\n"))
					m.Flush()

					var whole strings.Builder
					m = New(&whole, size, "monokai")
					m.Col = start
					m.Write([]byte(string(line) + "\n"))
					m.Flush()

					got, want := newTerm(cols, height), newTerm(cols, height)
					got.write(t, before+prefix)
					got.write(t, out.String())
					want.write(t, before+prefix)
					want.write(t, whole.String())
					bad := strings.Count(out.String(), "\x1b[J") != strings.Count(out.String(), "\x1b[B\x1b[J")
					if bad || strings.Join(got.lines(), "\n") != strings.Join(want.lines(), "\n") ||
						strings.Join(got.history, "\n") != strings.Join(want.history, "\n") || got.x != want.x || got.y != want.y {
						failed[key] = true
						t.Errorf("%s, %q:\nscreen  %q\nwant    %q\nhistory %q\nwant    %q\ncursor at %d,%d, want %d,%d\noutput %q",
							key, string(line), got.lines(), want.lines(), got.history, want.history, got.x, got.y, want.x, want.y, out.String())
					}
				}
			}
		}
	}
}
