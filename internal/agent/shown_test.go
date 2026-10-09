package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/tools"
)

// A command with the sequences of a terminal in it shows them as signs
// above the question, which is about the command that runs; the shell gets
// it as the model wrote it.
func TestShownCommandAsked(t *testing.T) {
	const command = "rm -rf ~/proj #\x1b[2K\r\x1b[36m❯\x1b[39m \x1b[1mls\x07\tx"
	pol, err := policy.Load(context.Background(), "", policy.Rules{Ask: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", string(args))}},
	}}
	a, _, sh, ui, cwd := newAgent(t, prov)
	a.Policy, ui.answer, ui.cols = pol, "y", 80
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	shown := capture.Clean(ui.Bytes())
	if want := "❯ rm -rf ~/proj #␛[2K␍␛[36m❯␛[39m ␛[1mls␇ x"; !strings.HasSuffix(shown, want) {
		t.Errorf("shown %q, want %q in it", shown, want)
	}
	if s := ui.String(); strings.Contains(s, "\x1b[2K") || strings.Contains(s, "\x1b[39m") || strings.ContainsAny(s, "\a\t") {
		t.Errorf("the model's sequences reached the terminal: %q", s)
	}
	if len(ui.asked) != 1 || ui.asked[0] != bold+`matches "rm *" — allow?`+reset {
		t.Errorf("asked %q", ui.asked)
	}
	if len(sh.handed) != 1 || sh.handed[0] != "c1\x00"+command {
		t.Errorf("handed off %q, want %q", sh.handed, command)
	}
}

// The reason of a hook shows its lines and signs for the rest, asked or
// denied; the model gets it as the hook gave it.
func TestShownReason(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		asked       string
		shown       string
	}{
		{"ask", `{"action":"ask","reason":"deploys\u001b[1A\u001b[2K\r\nall clear\rnow"}`,
			bold + "deploys␛[1A␛[2K\nall clear\nnow — allow?" + reset, ""},
		{"deny", `{"action":"deny","reason":"no\u001b[2K\u0008\u0008ok"}`,
			"", "✗ denied by hook h: no␛[2K␈␈ok\n"},
	} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"make deploy"}`)}},
			{Text: "done"},
		}}
		a, j, _, ui, cwd := newAgent(t, prov)
		a.Cfg.HooksDir, ui.answer, ui.cols = "", "n", 80
		hook(t, a, "pre-tool", "h", "cat >/dev/null; echo '"+tc.reply+"'")
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		if tc.asked != "" && (len(ui.asked) != 1 || ui.asked[0] != tc.asked) {
			t.Errorf("%s: asked %q, want %q", tc.name, ui.asked, tc.asked)
		}
		if shown := capture.Clean(ui.Bytes()); !strings.Contains(shown, tc.shown) {
			t.Errorf("%s: shown %q, want %q in it", tc.name, shown, tc.shown)
		}
		if s := ui.String(); strings.Contains(s, "\x1b[2K") || strings.Contains(s, "\x1b[1A") || strings.ContainsAny(s, "\b") {
			t.Errorf("%s: the hook's sequences reached the terminal: %q", tc.name, s)
		}
		if tc.name == "deny" {
			if r := j.es[2]; r.Output != "denied by hook h: no\x1b[2K\b\bok" {
				t.Errorf("deny: result %q", r.Output)
			}
		}
	}
}

// The title of a call is the model's text too: on the screen and in the
// folds the proxy keeps for Ctrl+O.
func TestShownTitle(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"lo\u001b[2Kng.txt"}`)}},
		{Text: "done"},
	}}
	a, _, _, ui, cwd := newAgent(t, prov)
	ui.cols = 80
	if err := os.WriteFile(filepath.Join(cwd, "lo\x1b[2Kng.txt"), []byte("1\n2\n3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(ui.folds) != 1 || ui.folds[0] != "⚙ read_file lo␛[2Kng.txt" {
		t.Errorf("folds %q", ui.folds)
	}
	if s := ui.String(); strings.Contains(s, "\x1b[2K") {
		t.Errorf("the model's sequences reached the terminal: %q", s)
	}
}

// The column of the status counts the signs and the spaces of a tab.
func TestRenderBashVisible(t *testing.T) {
	text, col, long, _ := renderBash("ls\x1b[2K\tx", 80, true)
	if got := capture.Clean([]byte(text)); got != "❯ ls␛[2K  x" || col != 11 || long {
		t.Errorf("%q: col %d, long %v", got, col, long)
	}
	text, _, _, _ = renderBash("a\r\nb\x1b[A", 80, false)
	if got := capture.Clean([]byte(text)); got != "❯ a␍\n  b␛[A" {
		t.Errorf("closed: %q", got)
	}
}
