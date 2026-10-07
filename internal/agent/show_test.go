package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/tools"
)

// A call line left open for the status is one line, cut with "…" to
// leave room for the short status at the right edge.
func TestRenderCallCut(t *testing.T) {
	long := "read_file " + strings.Repeat("/very/long/path", 10)
	room := runewidth.StringWidth(shortStatus) + 1
	text, col := renderCall(long, 80, true)
	got := capture.Clean([]byte(text))
	if strings.Contains(text, "\n") || !strings.HasPrefix(got, "⚙ read_file /very/long") || !strings.HasSuffix(got, "…") {
		t.Errorf("cut %q", got)
	}
	if w := runewidth.StringWidth(got); col != w || w > 80-room || w < 80-room-1 {
		t.Errorf("col %d, width %d; want %d", col, w, 80-room)
	}

	text, col = renderCall("read_file notes.txt", 80, true)
	if got := capture.Clean([]byte(text)); got != "⚙ read_file notes.txt" || col != runewidth.StringWidth(got) {
		t.Errorf("short title: %q, col %d", got, col)
	}

	// Too narrow for both: the line takes the whole width, the status
	// goes below.
	text, col = renderCall(long, 40, true)
	if w := runewidth.StringWidth(capture.Clean([]byte(text))); col != w || w > 40 || w < 39 {
		t.Errorf("narrow: col %d, width %d", col, w)
	}

	// Control characters would put the status off its column.
	text, _ = renderCall("probe a\tb\x1b[31m", 80, true)
	if got := strings.TrimPrefix(text, cyan+"⚙"+reset+" "); got != "probe a b [31m" {
		t.Errorf("control characters: %q", got)
	}
}

// A closed line, or one with no terminal to measure, is the whole title.
func TestRenderCallClosed(t *testing.T) {
	long := "read_file " + strings.Repeat("/very/long/path", 10)
	for _, tc := range []struct {
		cols int
		open bool
	}{{80, false}, {0, true}} {
		text, col := renderCall(long, tc.cols, tc.open)
		if got := capture.Clean([]byte(text)); got != "⚙ "+long || !strings.HasSuffix(text, "\n") || col != -1 {
			t.Errorf("cols %d, open %v: %q, col %d", tc.cols, tc.open, got, col)
		}
	}
}

// orderUI records, in order, what the agent tells the UI besides text.
type orderUI struct {
	fakeUI
	calls []string
}

// Fold ends the line with the status, as the UI must: the spinner of the
// next turn takes the cursor for the start of a line and would draw over
// a call line left open.
func (u *orderUI) Fold(title, text string) {
	u.calls = append(u.calls, "fold "+title)
	u.fakeUI.Fold(title, text)
	u.WriteString("[status]\n")
}

func (u *orderUI) Live(title string) Live {
	u.calls = append(u.calls, "live "+title)
	return u.fakeUI.Live(title)
}

func (u *orderUI) CommandAt(col int, long bool, hidden int) {
	u.calls = append(u.calls, fmt.Sprintf("at %d %v %d", col, long, hidden))
}

// callAgent runs a request of one call on an 80-column terminal, with
// notes.txt of one line, long.txt of three and the external tool probe.
func callAgent(t *testing.T, call llm.ToolCall, setup func(*Agent, *orderUI)) (*orderUI, string) {
	t.Helper()
	prov := &fakeProvider{replies: []*llm.Response{{ToolCalls: []llm.ToolCall{call}}, {Text: "done"}}}
	a, _, _, _, cwd := newAgent(t, prov)
	for name, body := range map[string]string{"notes.txt": "hello\n", "long.txt": "1\n2\n3\n"} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "probe"), []byte("#!/bin/sh\n# aish:desc Probe\necho probed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Tools = tools.Load(cwd)
	ui := &orderUI{fakeUI: fakeUI{cols: 80}}
	a.UI = ui
	if setup != nil {
		setup(a, ui)
	}
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	return ui, capture.Clean(ui.Bytes())
}

// The line of a call is left open for the status the UI puts at its right
// edge, and the UI learns where it ends right before the status is due.
// Anything that is not the status ends the line first.
func TestShowCallOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		call  llm.ToolCall
		setup func(*Agent, *orderUI)
		calls string
		shown string // the line of the call and what follows it
	}{
		{"long result", toolCall("c1", "read_file", `{"path":"long.txt"}`), nil,
			"at 20 false 0, fold ⚙ read_file long.txt", "⚙ read_file long.txt[status]\ndone"},
		{"one-line result", toolCall("c1", "read_file", `{"path":"notes.txt"}`), nil,
			"", "⚙ read_file notes.txt\n  1\thello\n"},
		{"error", toolCall("c1", "read_file", `{"path":"missing.txt"}`), nil,
			"", "⚙ read_file missing.txt\n  ✗ "},
		{"external", toolCall("c1", "probe", `{}`), nil,
			"at 7 false 0, live ⚙ probe", "⚙ probeprobed\n"},
		{"fold_lines", toolCall("c1", "read_file", `{"path":"long.txt"}`), func(a *Agent, _ *orderUI) { a.Cfg.FoldLines = 3 },
			"fold ⚙ read_file long.txt", "⚙ read_file long.txt\n[status]\ndone"},
		{"replaced by a hook", toolCall("c1", "read_file", `{"path":"notes.txt"}`), func(a *Agent, _ *orderUI) {
			a.Cfg.HooksDir = ""
			hook(t, a, "pre-tool", "h", `cat >/dev/null; echo '{"args":{"path":"long.txt"}}'`)
		}, "at 20 false 0, fold ⚙ read_file long.txt", "(arguments replaced by pre-tool/h)\n⚙ read_file long.txt[status]\ndone"},
	} {
		ui, out := callAgent(t, tc.call, tc.setup)
		if got := strings.Join(ui.calls, ", "); got != tc.calls {
			t.Errorf("%s: calls %q, want %q", tc.name, got, tc.calls)
		}
		if !strings.Contains(out, tc.shown) || strings.Count(out, "⚙") != 1 {
			t.Errorf("%s: terminal %q, want %q in it", tc.name, out, tc.shown)
		}
	}
}

// A call the user is asked about is shown whole, before the question.
func TestShowCallAsked(t *testing.T) {
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n@ask(\"sure?\") forbid(principal, action, resource) when { context.tool == \"read_file\" };\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	ui, out := callAgent(t, toolCall("c1", "read_file", `{"path":"long.txt"}`), func(a *Agent, ui *orderUI) {
		a.Policy, ui.answer = pol, "y"
	})
	if got := strings.Join(ui.calls, ", "); got != "fold ⚙ read_file long.txt" || len(ui.asked) != 1 {
		t.Errorf("calls %q, asked %q", got, ui.asked)
	}
	if !strings.Contains(out, "⚙ read_file long.txt\n") {
		t.Errorf("terminal %q", out)
	}
}
