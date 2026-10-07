package proxy

import (
	"io"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/session"
)

// A command's title in the viewer takes a line for each of its lines,
// indented after the first as on the screen, all of them in the title color.
func TestViewerMultilineTitle(t *testing.T) {
	v := newViewer([]Fold{{Title: "❯ a\nb\nc", Text: "1\n"}}, 20, 10)
	if len(v.rows) != 4 || v.top != 0 {
		t.Fatalf("rows %q, top %d", v.rows, v.top)
	}
	for i := 0; i < 3; i++ {
		if !v.title[i] {
			t.Errorf("row %d (%q) is no title", i, v.rows[i])
		}
	}
	if v.rows[1] != "  b" || v.rows[3] != "1" || v.title[3] {
		t.Errorf("rows %q, titles %v", v.rows, v.title)
	}

	// The viewer opens on the first line of the last title.
	v = newViewer([]Fold{{Title: "❯ x", Text: strings.Repeat("y\n", 20)}, {Title: "❯ a\nb", Text: strings.Repeat("z\n", 20)}}, 20, 5)
	if v.rows[v.top] != "❯ a" || v.rows[v.top+1] != "  b" {
		t.Errorf("top %d: %q", v.top, v.rows[v.top])
	}

	// A command with no output is its title alone.
	v = newViewer([]Fold{{Title: "❯ a\nb"}}, 20, 5)
	if len(v.rows) != 2 {
		t.Errorf("no output: rows %q", v.rows)
	}
}

// cutProxy is a proxy where the agent has printed a command, cut short by
// hidden lines, and the shell runs it without printing anything.
func cutProxy(t *testing.T, hidden int) *Proxy {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = io.Discard
	p.size = func() (int, int) { return 80, 24 }
	p.marker(Marker{Kind: "ask-start"})
	(&ui{p: p}).CommandAt(14, true, hidden)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;cat > f <<EOF\na\nb\nc\nd\nEOF"})
	return p
}

// A command the agent cut short on the screen is kept for Ctrl+O even
// without output: its title is the only place it shows whole.
func TestCutCommandKept(t *testing.T) {
	p := cutProxy(t, 7)
	folds := p.viewFolds()
	if len(folds) != 1 || !strings.HasSuffix(folds[0].Title, "(running)") {
		t.Errorf("while running: %+v", folds)
	}
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	if len(p.folds) != 1 || p.folds[0].Title != "❯ cat > f <<EOF\na\nb\nc\nd\nEOF" {
		t.Errorf("folds %+v", p.folds)
	}

	p = cutProxy(t, 0)
	if folds := p.viewFolds(); len(folds) != 0 {
		t.Errorf("whole command while running: %+v", folds)
	}
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	if len(p.folds) != 0 {
		t.Errorf("whole command, no output: %+v", p.folds)
	}
}
