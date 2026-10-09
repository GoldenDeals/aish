package proxy

import (
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/capture"
)

// A viewer that shows other folds lays out only those from the first that
// changed, and comes to the rows a new viewer of them has.
func TestViewerLayoutFrom(t *testing.T) {
	a := Fold{Title: "❯ a", Text: "1\n2\n"}
	b := Fold{Title: "❯ b", Text: strings.Repeat("x", 30)}
	c := Fold{Title: "❯ c"}
	ask := askFold("next")
	v := newViewer([]Fold{a, b}, 20, 10)
	for _, folds := range [][]Fold{
		{a, b, ask, c},
		{a, {Title: "❯ b", Text: "y"}, ask, c},
		{a, ask},
		{a, ask, b},
		{a},
		{},
		{ask, a},
	} {
		v.update(folds)
		w := newViewer(folds, 20, 10)
		if !slices.Equal(v.rows, w.rows) || !slices.Equal(v.title, w.title) || !slices.Equal(v.sep, w.sep) || !slices.Equal(v.start, w.start) {
			t.Errorf("%q:\nrows %q title %v sep %v start %v, want\nrows %q title %v sep %v start %v",
				folds, v.rows, v.title, v.sep, v.start, w.rows, w.title, w.sep, w.start)
		}
	}
}

// The viewer opens at the last output, under the line of its request when
// it is the request's first.
func TestViewerOpensAtRequest(t *testing.T) {
	long := Fold{Title: "❯ seq", Text: numbered(30)}
	v := newViewer([]Fold{long, askFold("again"), long}, 20, 10)
	if v.rows[v.top] != "? again" {
		t.Errorf("opens at %q", v.rows[v.top])
	}
	v = newViewer([]Fold{askFold("again"), long, long}, 20, 10)
	if v.top != v.start[2] {
		t.Errorf("opens at row %d, want %d", v.top, v.start[2])
	}
}

// cleanText is capture.Clean, the newlines at the end left out, whether
// the text has anything to clean or not.
func FuzzCleanText(f *testing.F) {
	for _, s := range []string{
		"", "a", "a\n", "a\n\n\nb\n\n", "a \n", "a ", " a", "a\tb\n", "日本語\n",
		"\x1b[1mbold\x1b[0m", "a\rb", "a\bb", "\xff", "a\x7fb", "\x01", "\u0085", "\n\n",
		"x\x1b[?1049hvim\x1b[?1049ly\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := cleanText(s), strings.TrimRight(capture.Clean([]byte(s)), "\n"); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", s, got, want)
		}
	})
}

// Printable ASCII wraps a column a byte.
func TestWrapASCII(t *testing.T) {
	for _, c := range []struct {
		l    string
		want []string
	}{
		{"", []string{""}},
		{"abc", []string{"abc"}},
		{"abcdefghij", []string{"abc", "def", "ghi", "j"}},
		{"abcdef", []string{"abc", "def"}},
		{"ab日", []string{"ab", "日"}},
	} {
		if got := wrapWidth(c.l, 3); !slices.Equal(got, c.want) {
			t.Errorf("wrapWidth(%q, 3) = %q, want %q", c.l, got, c.want)
		}
	}
}
