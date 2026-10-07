package proxy

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// After a summary, till the next turn is measured, the estimate carries
// the system prompt and the tool schemas the agent will send with it; a
// measured turn has them counted already.
func TestContextTokensOverhead(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.overhead = 1000
	if n, measured := p.contextTokens(nil); n != 0 || measured {
		t.Errorf("an empty journal: %d, measured %v", n, measured)
	}
	es := []session.Entry{
		{Kind: session.KindUser, Text: "q"},
		{Kind: session.KindAssistant, Text: "a", InputTokens: 50_000},
		{Kind: session.KindSummary, Text: "what was done"},
		{Kind: session.KindShell, Cmd: "ls", Output: "a b c\n"},
		{Kind: session.KindShell, Cmd: "pwd", Output: "/home\n"},
	}
	want := session.Tokens(es, p.maxOutput)
	if n, measured := p.contextTokens(es); n != want+1000 || measured {
		t.Errorf("after a summary: %d, measured %v; want %d+1000", n, measured, want)
	}
	es = append(es, session.Entry{Kind: session.KindAssistant, Text: "b", InputTokens: 3000})
	want = session.Tokens(es, p.maxOutput)
	if n, measured := p.contextTokens(es); n != want || !measured {
		t.Errorf("after a measured turn: %d, measured %v; want %d", n, measured, want)
	}
	if n, _ := p.contextTokens([]session.Entry{{Kind: session.KindClear}}); n != 0 {
		t.Errorf("a cleared journal: %d", n)
	}
}

// The status at the prompt and `aish status` count the overhead: compact?
// goes on when the agent would compact, not a turn later.
func TestStatusCountsOverhead(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.model, p.window, p.compactAt = "m", 10_000, 0.5
	if err := sess.Append(session.Entry{Kind: session.KindSummary, Text: strings.Repeat("s", 4000)}); err != nil {
		t.Fatal(err)
	}
	if text, _ := p.statusText(); strings.Contains(text, "compact?") {
		t.Errorf("no overhead known: %q", text)
	}
	p.overhead = 5000
	if text, _ := p.statusText(); !strings.Contains(text, "compact?") {
		t.Errorf("with the overhead: %q", text)
	}
	want := session.Tokens(sess.Entries(), p.maxOutput) + 5000
	if st := p.status(); st.Tokens != want || st.Measured {
		t.Errorf("aish status: %d tokens, measured %v; want %d", st.Tokens, st.Measured, want)
	}
}

// A request leaves the proxy what its system prompt and tools weigh.
func TestRequestKeepsOverhead(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{{Text: "hi"}}}
	p, _, cwd := hosted(t, prov)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hello", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	got := p.overhead
	p.mu.Unlock()
	if want := p.ag.Overhead(); got == 0 || got != want {
		t.Errorf("overhead %d, the agent's %d", got, want)
	}
}
