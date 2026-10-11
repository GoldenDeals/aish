package plaintext

import (
	"math"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestSize(t *testing.T) {
	b := New(nil)
	if _, h := b.Size(5, math.MaxInt); h != 0 {
		t.Errorf("empty: %d rows", h)
	}
	b.Write("hello wor")
	b.Write("ld foo") // streamed: one text
	for w, want := range map[int]int{5: 3, 20: 1} {
		if _, h := b.Size(w, math.MaxInt); h != want {
			t.Errorf("width %d: %d rows, want %d", w, h, want)
		}
	}
}

func TestControlDropped(t *testing.T) {
	b := New(nil)
	b.Write("a\x1b[31mb\tc\r\n\u009bd\x07")
	scr := uv.NewWindow(20, 2, nil)
	b.Draw(scr, scr.Bounds())
	if got, want := scr.String(), "a[31mb    c\nd"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDrawCut(t *testing.T) {
	b := New(nil)
	b.Write("one two three") // at width 5: one, two, three
	scr := uv.NewWindow(5, 2, nil)
	b.Draw(scr, uv.Rect(0, -1, 5, 3)) // the first row is above the screen
	if got, want := scr.String(), "two\nthree"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
