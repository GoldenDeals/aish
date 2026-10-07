package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/capture"
)

// script is a command of n lines: "line 1" to "line n".
func script(n int) []string {
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	return lines
}

// screen is how lines show on the terminal: the first after "❯ ", the
// others indented.
func screen(lines ...string) string { return "❯ " + strings.Join(lines, "\n  ") }

// A long command left open for the status shows its first lines and how
// many more there are; the status goes after that count.
func TestRenderBashCut(t *testing.T) {
	text, col, long, hidden := renderBash(strings.Join(script(10), "\n"), 80, true)
	if strings.HasSuffix(text, "\n") {
		t.Errorf("the line is not left open: %q", text)
	}
	if got, want := capture.Clean([]byte(text)), screen("line 1", "line 2", "line 3", "… (+7 lines)"); got != want {
		t.Errorf("shown %q, want %q", got, want)
	}
	if col != 14 || !long || hidden != 7 {
		t.Errorf("col %d, long %v, hidden %d; want 14, true, 7", col, long, hidden)
	}

	text, _, _, hidden = renderBash(strings.Join(script(4), "\n"), 80, true)
	if got, want := capture.Clean([]byte(text)), screen("line 1", "line 2", "line 3", "… (+1 line)"); got != want || hidden != 1 {
		t.Errorf("4 lines: %q, hidden %d; want %q", got, hidden, want)
	}
}

func TestRenderBashWhole(t *testing.T) {
	text, col, long, hidden := renderBash(strings.Join(script(3), "\n"), 80, true)
	if got, want := capture.Clean([]byte(text)), screen(script(3)...); got != want {
		t.Errorf("3 lines: %q, want %q", got, want)
	}
	if col != len("  line 3") || !long || hidden != 0 {
		t.Errorf("3 lines: col %d, long %v, hidden %d", col, long, hidden)
	}

	// A closed line (asked about, or folded from a later line) is never cut.
	text, col, _, hidden = renderBash(strings.Join(script(10), "\n"), 80, false)
	if got, want := capture.Clean([]byte(text)), screen(script(10)...); got != want || !strings.HasSuffix(text, "\n") {
		t.Errorf("closed: %q, want %q", text, want)
	}
	if col != -1 || hidden != 0 {
		t.Errorf("closed: col %d, hidden %d", col, hidden)
	}
}

// Trailing newlines of a command are no lines of it.
func TestRenderBashTrailingNewline(t *testing.T) {
	text, col, long, hidden := renderBash("ls -l\n", 80, true)
	if got := capture.Clean([]byte(text)); got != "❯ ls -l" || strings.HasSuffix(text, "\n") || col != 7 || long || hidden != 0 {
		t.Errorf("%q: col %d, long %v, hidden %d", text, col, long, hidden)
	}
	text, _, _, hidden = renderBash(strings.Join(script(4), "\n")+"\n\n", 80, true)
	if got, want := capture.Clean([]byte(text)), screen("line 1", "line 2", "line 3", "… (+1 line)"); got != want || hidden != 1 {
		t.Errorf("%q, hidden %d; want %q", got, hidden, want)
	}
	text, _, _, _ = renderBash("a\nb\n", 80, false)
	if got := capture.Clean([]byte(text)); got != screen("a", "b") || strings.HasSuffix(text, "\n\n") || strings.Contains(text, "\n  \n") {
		t.Errorf("closed: %q", text)
	}
}

// showBash tells the UI how many lines it left out.
func TestShowBashHidden(t *testing.T) {
	ui := &hiddenUI{fakeUI: fakeUI{cols: 80}}
	a := &Agent{UI: ui}
	a.showBash(strings.Join(script(5), "\n"), false)
	if ui.col != 14 || !ui.long || ui.hidden != 2 {
		t.Errorf("CommandAt(%d, %v, %d)", ui.col, ui.long, ui.hidden)
	}
	if got := capture.Clean(ui.Bytes()); strings.Contains(got, "line 4") {
		t.Errorf("printed %q", got)
	}
}

type hiddenUI struct {
	fakeUI
	col, hidden int
	long        bool
}

func (u *hiddenUI) CommandAt(col int, long bool, hidden int) {
	u.col, u.long, u.hidden = col, long, hidden
}
