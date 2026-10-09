package capture

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// TestTextLineEdits checks textLine against the slice Clean edited before
// it, on lines long enough for the edits to cut them into pieces.
func TestTextLineEdits(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var l textLine
	for round := range 300 {
		l.reset()
		var m []rune
		for step := range 1500 {
			col := r.IntN(len(m) + 40)
			n := []int{1, 2, 3, 63, 64, 65, shiftMax, shiftMax + 1, 700, 3000}[r.IntN(10)]
			switch r.IntN(12) {
			case 0, 1, 2, 3: // text written over and after the line
				for k := range r.IntN(n) + 1 {
					c := rune('a' + (step+k)%26)
					l.put(col+k, c)
					for len(m) < col+k {
						m = append(m, ' ')
					}
					if col+k < len(m) {
						m[col+k] = c
					} else {
						m = append(m, c)
					}
				}
			case 4, 5:
				n = min(n, farCol)
				l.insert(col, n)
				if col < len(m) {
					m = slices.Insert(m, col, slices.Repeat([]rune{' '}, n)...)
				}
			case 6, 7:
				l.remove(col, n)
				if col < len(m) {
					m = slices.Delete(m, col, min(col+n, len(m)))
				}
			case 8:
				l.blank(col, col+n)
				for j := col; j < min(col+n, len(m)); j++ {
					m[j] = ' '
				}
			case 9:
				l.blank(0, col+1)
				for j := 0; j <= col && j < len(m); j++ {
					m[j] = ' '
				}
			case 10:
				if r.IntN(30) == 0 {
					l.truncate(col)
					m = m[:min(col, len(m))]
				}
			}
			if l.len() != len(m) {
				t.Fatalf("round %d step %d: %d runes, want %d", round, step, l.len(), len(m))
			}
			if step%100 == 0 && l.String() != string(m) {
				t.Fatalf("round %d step %d: line differs", round, step)
			}
		}
		if l.String() != string(m) {
			t.Fatalf("round %d: line differs", round)
		}
	}
}

func TestCleanLongLineEdits(t *testing.T) {
	const n = 12000
	got := Clean([]byte(strings.Repeat("a\x1b[D\x1b[99@", n)))
	want := strings.Repeat(" ", 99) + strings.Repeat("a"+strings.Repeat(" ", 98), n-1) + "a"
	if got != want {
		t.Errorf("inserts: %d bytes, want %d", len(got), len(want))
	}
	text := strings.Repeat("0123456789", 5400)
	got = Clean([]byte(text + "\r" + strings.Repeat("\x1b[P", 13501) + "\x1b[2C\x1b[999999X"))
	if want := text[13501 : 13501+2]; got != want {
		t.Errorf("deletes: %q, want %q", got, want)
	}
}

// BenchmarkCleanLongLine edits at the start of a long line, which each edit
// went all along before.
func BenchmarkCleanLongLine(b *testing.B) {
	long := strings.Repeat("0123456789", 5400) + "\r"
	for _, c := range []struct{ name, in string }{
		{"insert", strings.Repeat("a\x1b[D\x1b[99@", 12000)},
		{"delete", long + strings.Repeat("\x1b[P", 13500)},
		{"erase", long + strings.Repeat("\x1b[99999X", 6000)},
		{"plain", strings.Repeat("0123456789", 10800)},
	} {
		in := []byte(c.in)
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			for b.Loop() {
				Clean(in)
			}
		})
	}
}
