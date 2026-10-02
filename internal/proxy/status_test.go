package proxy

import (
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

// Past compact_at the status says the next request starts with a summary.
func TestStatusCompactMark(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.model, p.window, p.compactAt = "m", 1000, 0.8
	if err := sess.Append(session.Entry{Kind: session.KindAssistant, Text: "a", InputTokens: 700}); err != nil {
		t.Fatal(err)
	}
	if text, _ := p.statusText(); text != "700/1.0k 70% · m" {
		t.Errorf("below the limit: %q", text)
	}
	if err := sess.Append(session.Entry{Kind: session.KindAssistant, Text: "b", InputTokens: 850}); err != nil {
		t.Fatal(err)
	}
	if text, _ := p.statusText(); text != "850/1.0k 85% compact? · m" {
		t.Errorf("past the limit: %q", text)
	}
	p.compactAt = 0
	if text, _ := p.statusText(); text != "850/1.0k 85% · m" {
		t.Errorf("compact_at = 0: %q", text)
	}
}

// With no context_window in the config the agent compacts by the window
// the shell knows, from the API or `aish model`; the mark goes with it.
func TestAutoCompactByShellWindow(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{{Text: "we said hi"}, {Text: "ok"}}}
	p, out, cwd := hosted(t, prov)
	p.window, p.compactAt = 32000, 0.8
	if err := p.sess.Append(
		session.Entry{Kind: session.KindUser, Text: "hi", Cwd: cwd},
		session.Entry{Kind: session.KindAssistant, Text: "hello", InputTokens: 30000},
	); err != nil {
		t.Fatal(err)
	}
	if text, _ := p.statusText(); !strings.Contains(text, "compact?") {
		t.Errorf("status before %q", text)
	}
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "again", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := journalKinds(p.sess); got != "user assistant user summary user assistant" {
		t.Fatalf("journal %s", got)
	}
	if s := out.String(); !strings.Contains(s, "compacted: 30k → ") {
		t.Errorf("terminal %q", s)
	}
	if text, _ := p.statusText(); strings.Contains(text, "compact?") {
		t.Errorf("status after %q", text)
	}
}
