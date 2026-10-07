package proxy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/session"
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

// drawnAt is where a status of width w is drawn at the right edge of a
// terminal cols wide: the last column stays free, as with the prompt's.
func drawnAt(w, cols int) string { return fmt.Sprintf("\x1b[%dG", cols-w) }

// statusOf is a status as drawn, with its gap before it.
func statusOf(parts ...string) string { return "  (" + strings.Join(parts, " · ") + ")" }

// The status goes on the line of the call, at the right edge: in full
// when it fits between the call and the edge, short when only that
// does, on a line of its own below the call otherwise.
func TestFoldStatusRight(t *testing.T) {
	full := statusOf("3 lines", "exit 1", "ctrl+o to expand")
	short := statusOf("3 lines", "exit 1")
	fw, sw := runewidth.StringWidth(full), runewidth.StringWidth(short)
	right := "\r\x1b[7C\x1b[K\x1b[?7l"
	below := "\r\n\r\x1b[K\x1b[?7l"
	for _, tc := range []struct {
		name      string
		col, cols int
		start     string // what comes before the status
		text      string
	}{
		{"full", 7, 7 + fw + 1, right + drawnAt(fw, 7+fw+1), full},
		{"short", 7, 7 + fw, right + drawnAt(sw, 7+fw), short},
		{"below", 7, 7 + sw, below, short},
		{"below in full", fw - sw, fw, below, full},
	} {
		f := newFold("❯ seq 3", 0)
		f.at = &statusAt{col: tc.col, cols: tc.cols}
		f.hiddenLines = 3
		if got, want := string(f.finish(1)), tc.start+"\x1b[0m"+dim+tc.text+reset+"\x1b[?7h\r\n"; got != want {
			t.Errorf("%s: status\n%q, want\n%q", tc.name, got, want)
		}
	}

	// Drawn at the right while the command ran, the status moves below
	// when it outgrows the room.
	f := newFold("❯ seq 3", 0)
	f.at = &statusAt{col: 7, cols: 7 + sw}
	if got := string(f.write([]byte("1\n2\n3\n"))); !strings.HasPrefix(got, right) {
		t.Errorf("running: %q", got)
	}
	if got, want := string(f.finish(1)), "\r\x1b[7C\x1b[K"+below+"\x1b[0m"+dim+short+reset+"\x1b[?7h\r\n"; got != want {
		t.Errorf("outgrown: status\n%q, want\n%q", got, want)
	}
}

// A command of several lines gets the status of any other, hint and all;
// one cut short has something to expand even without output.
func TestFoldStatusLong(t *testing.T) {
	long := newFold("❯ a\n  b", 0)
	long.at = &statusAt{col: 3, cols: 80}
	long.write([]byte("1\n2\n"))
	if got := capture.Clean(long.finish(0)); !strings.Contains(got, "(2 lines · ctrl+o to expand)") {
		t.Errorf("long command status %q", got)
	}
	whole := newFold("❯ a\n  b", 0)
	whole.at = &statusAt{col: 3, cols: 80}
	if got := capture.Clean(whole.finish(0)); !strings.Contains(got, "(no output)") || strings.Contains(got, "ctrl+o") {
		t.Errorf("whole command, no output: %q", got)
	}
	cut := newFold("❯ cat > f <<EOF\na\nb\nc\nd\nEOF", 0)
	cut.at = &statusAt{col: 14, cols: 80, hidden: 3}
	if got := capture.Clean(cut.finish(0)); !strings.Contains(got, "(no output · ctrl+o to expand)") {
		t.Errorf("cut command, no output: %q", got)
	}
}

// statusProxy is a proxy on a terminal cols wide, inside a request.
func statusProxy(t *testing.T, cols int) (*Proxy, *terminal) {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	p.size = func() (int, int) { return cols, 24 }
	p.marker(Marker{Kind: "ask-start"})
	return p, out
}

// A built-in's long result has its status drawn by the proxy, at the
// right edge of the call when the agent left its line open, on a line of
// its own when it did not.
func TestFoldResultStatus(t *testing.T) {
	p, out := statusProxy(t, 80)
	u := &ui{p: p}
	u.CommandAt(22, false, 0)
	u.Fold("⚙ read_file notes.txt", "1\n2\n3\n")
	text := statusOf("3 lines", "ctrl+o to expand")
	want := "\r\x1b[22C\x1b[K\x1b[?7l" + drawnAt(runewidth.StringWidth(text), 80) + "\x1b[0m" + dim + text + reset + "\x1b[?7h\r\n"
	if got := out.String(); got != want {
		t.Errorf("status\n%q, want\n%q", got, want)
	}
	if p.at != nil || len(p.folds) != 1 || p.folds[0].Text != "1\n2\n3\n" {
		t.Errorf("at %v, folds %+v", p.at, p.folds)
	}

	p, out = statusProxy(t, 80)
	(&ui{p: p}).Fold("⚙ read_file notes.txt", "1\n2\n")
	if got := out.String(); got != "\r\x1b[K\x1b[0m"+dim+statusOf("2 lines", "ctrl+o to expand")+reset+"\r\n" {
		t.Errorf("status of its own %q", got)
	}
}

// An external tool's status goes at the right of its call as a
// command's does; without output it says so.
func TestLiveStatusRight(t *testing.T) {
	p, out := statusProxy(t, 80)
	u := &ui{p: p}
	u.CommandAt(9, false, 0)
	l := u.Live("⚙ probe")
	l.Finish(-1)
	text := statusOf("no output")
	if got := out.String(); !strings.HasPrefix(got, "\r\x1b[9C\x1b[K\x1b[?7l"+drawnAt(runewidth.StringWidth(text), 80)) || !strings.Contains(got, text) {
		t.Errorf("no output: %q", got)
	}
	if p.at != nil || len(p.folds) != 0 {
		t.Errorf("at %v, folds %+v", p.at, p.folds)
	}

	// Lines shown before folding go below the call.
	p, out = statusProxy(t, 80)
	p.foldLines = 3
	u = &ui{p: p}
	u.CommandAt(9, false, 0)
	l = u.Live("⚙ probe")
	l.Write([]byte("one\n"))
	l.Finish(0)
	if got := out.String(); got != "\r\none\r\n" {
		t.Errorf("fold_lines 3: %q", got)
	}
}
