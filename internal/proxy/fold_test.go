package proxy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/capture"
)

func TestScreen(t *testing.T) {
	cases := []struct {
		chunks []string
		want   []bool
	}{
		{[]string{"ls\r\n", "\x1b[H\x1b[2J\x1b[3J"}, []bool{false, true}},
		{[]string{"\x1b[H\x1b[", "2J"}, []bool{false, true}}, // split
		{[]string{"\x1bc"}, []bool{true}},
		// vim: erasing the alternate screen is not a clear
		{[]string{"\x1b[?1049h\x1b[2J", "text", "\x1b[?1049l"}, []bool{false, false, false}},
		{[]string{"\x1b[?10", "49h", "\x1b[2J", "\x1b[?1049l\x1b[2J"}, []bool{false, false, false, true}},
		{[]string{"\x1b[31mred\x1b[0m"}, []bool{false}},
	}
	for i, c := range cases {
		var s Screen
		for j, ch := range c.chunks {
			if got, _ := s.Feed([]byte(ch)); got != c.want[j] {
				t.Errorf("case %d chunk %d %q: cleared=%v, want %v", i, j, ch, got, c.want[j])
			}
		}
	}
}

func TestScreenFrom(t *testing.T) {
	var s Screen
	s.Feed([]byte("old\x1b[H\x1b[2J\x1b"))
	cleared, from := s.Feed([]byte("[3Jnew"))
	if !cleared || from != 3 {
		t.Fatalf("cleared=%v from=%d, want true 3", cleared, from)
	}
}

func TestFold(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	all := strings.Join(lines, "\n") + "\n"

	f := newFold("❯ seq 30", 5)
	var shown strings.Builder
	for i := 0; i < len(all); i += 7 { // odd-sized chunks
		shown.Write(f.write([]byte(all[i:min(i+7, len(all))])))
	}
	shown.Write(f.finish(-1))
	out := shown.String()
	head := strings.Join(lines[:5], "\n") + "\n"
	if !strings.HasPrefix(out, head) {
		t.Fatalf("head not shown:\n%q", out)
	}
	if strings.Contains(out, lines[5]+"\n"+lines[6]) {
		t.Fatalf("hidden lines leaked:\n%q", out)
	}
	if !strings.HasSuffix(out, "  (25 lines · ctrl+o to expand)\x1b[0m\r\n") {
		t.Fatalf("bad final status:\n%q", out)
	}
	if !f.folded() || string(f.raw.Bytes()) != all {
		t.Fatalf("raw output not kept")
	}

	// Short output passes through untouched.
	g := newFold("x", 5)
	if got := string(g.write([]byte("a\nb\n"))); got != "a\nb\n" || g.finish(-1) != nil {
		t.Fatalf("short output changed: %q", got)
	}

	// Ctrl+O while running shows the hidden part and stops folding.
	h := newFold("x", 2)
	h.write([]byte("1\n2\n3\n4\n"))
	if got := string(h.expand()); got != "\r\x1b[K3\n4\n" {
		t.Fatalf("expand = %q", got)
	}
	if got := string(h.write([]byte("5\n"))); got != "5\n" || h.finish(-1) != nil {
		t.Fatalf("after expand: %q", got)
	}
}

func TestViewer(t *testing.T) {
	folds := []Fold{{Title: "❯ a", Text: "1\r\n2\r\n"}, {Title: "❯ b", Text: strings.Repeat("x\n", 50)}}
	v := newViewer(folds, 20, 10)
	if v.top != 4 || !v.title[4] { // "❯ a", 1, 2, "", "❯ b"
		t.Fatalf("opens at row %d, want the last title", v.top)
	}
	if v.key([]byte("\x1b[C")) || v.key([]byte("j")) || v.top != 5 {
		t.Fatalf("unknown key closed the viewer or j did not scroll: top=%d", v.top)
	}
	if v.key([]byte("G")); v.top != len(v.rows)-v.page() {
		t.Fatalf("G: top=%d", v.top)
	}
	for _, k := range []string{"\x0f", "q", "\x1b", "\x03"} {
		if !v.key([]byte(k)) {
			t.Errorf("%q does not close", k)
		}
	}
	if w := newViewer([]Fold{{Title: "t", Text: strings.Repeat("y", 45)}}, 20, 10); len(w.rows) != 4 {
		t.Fatalf("wrapping: %d rows", len(w.rows))
	}
}

func TestFoldHidesAll(t *testing.T) {
	f := newFold("❯ seq 3", 0)
	var shown []byte
	shown = append(shown, f.write([]byte("1\n2\n3\n"))...)
	shown = append(shown, f.finish(1)...)
	got := capture.Clean(shown)
	if strings.Contains(got, "1\n") || !strings.Contains(got, "(3 lines · exit 1 · ctrl+o to expand)") {
		t.Fatalf("shown %q", got)
	}
	if string(f.raw.Bytes()) != "1\n2\n3\n" {
		t.Fatalf("raw %q", f.raw.Bytes())
	}
	e := newFold("❯ true", 0)
	if got := capture.Clean(e.finish(0)); !strings.Contains(got, "(no output)") {
		t.Fatalf("empty %q", got)
	}
}

func TestFoldStatusRight(t *testing.T) {
	f := newFold("❯ seq 3", 0)
	f.at = &statusAt{col: 7, cols: 60}
	shown := string(f.write([]byte("1\n2\n3\n"))) + string(f.finish(1))
	if !strings.HasPrefix(shown, "\r\x1b[7C\x1b[K") || !strings.HasSuffix(shown, "\r\n") {
		t.Fatalf("status not to the right: %q", shown)
	}
	if got := capture.Clean([]byte(shown)); !strings.Contains(got, "(3 lines · exit 1 · ctrl+o to expand)") {
		t.Errorf("status %q", got)
	}

	long := newFold("❯ a\n  b", 0)
	long.at = &statusAt{col: 3, cols: 60, long: true}
	if got := capture.Clean(long.finish(0)); !strings.Contains(got, "(no output)") || strings.Contains(got, "ctrl+o") {
		t.Errorf("long command status %q", got)
	}

	full := newFold("❯ x", 0)
	full.at = &statusAt{col: 55, cols: 60}
	if got := string(full.write([]byte("1\n2\n"))); !strings.HasPrefix(got, "\r\n\r\x1b[K") {
		t.Errorf("no room, status not below: %q", got)
	}
	if got := string(full.finish(0)); !strings.HasPrefix(got, "\r\x1b[K") {
		t.Errorf("no room, final status: %q", got)
	}
}
