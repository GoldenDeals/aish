package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// Right after a summary no turn has measured the context: the estimate
// adds the system prompt and the tool schemas, which the next turn's
// InputTokens will hold.
func TestContextTokens(t *testing.T) {
	a, _, _, _, cwd := newAgent(t, &fakeProvider{})
	a.Tools = &tools.Registry{}
	a.Tools.Add(probeTool{name: "probe"})
	a.Cfg.SystemPrompt = strings.Repeat("answer in verse; ", 100)
	a.exec = tools.Exec{Dir: cwd}
	es := []session.Entry{
		{Kind: session.KindUser, Text: "hi", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "hello", InputTokens: 20000, OutputTokens: 100},
		{Kind: session.KindSummary, Text: "we said hello", Cwd: cwd},
		{Kind: session.KindUser, Text: "and now?", Cwd: cwd},
	}
	a.entries = es

	req := a.request(nil)
	if len(req.Tools) != 1 || !strings.Contains(req.System, "answer in verse") {
		t.Fatalf("request %d tools, system %q", len(req.Tools), req.System)
	}
	over := overhead(req)
	if over < len(a.Cfg.SystemPrompt) || over != a.Overhead() {
		t.Fatalf("overhead %d bytes, Overhead %d", over, a.Overhead())
	}
	// The agent counts as the status and aish context do, by its own
	// max_output_bytes and overhead.
	want := session.Tokens(es, a.Cfg.MaxOutputBytes, over)
	if got := a.contextSize(es); got != want || got.Measured {
		t.Errorf("after a summary %+v, want %+v", got, want)
	}
	if bare := session.Tokens(es, a.Cfg.MaxOutputBytes, 0); want.Tokens < bare.Tokens+int(float64(over)/want.PerToken) {
		t.Errorf("after a summary %d tokens, without the overhead %d", want.Tokens, bare.Tokens)
	}

	es = append(es, session.Entry{Kind: session.KindAssistant, Text: "now this", InputTokens: 9000, OutputTokens: 50})
	if got := a.contextTokens(es); got != 9050 {
		t.Errorf("after a measured turn %d, want 9050", got)
	}
}

// compact_at goes by the window the agent has: none known, none past it.
func TestCompactLimit(t *testing.T) {
	cfg := config.Default()
	cfg.CompactAt, cfg.ContextWindow = 0.8, 200_000
	if got := CompactLimit(cfg); got != 160_000 {
		t.Errorf("limit %d", got)
	}
	cfg.ContextWindow = 0
	if got := CompactLimit(cfg); got != 0 {
		t.Errorf("limit %d of an unknown window", got)
	}
}

// Past compact_at right after a summary, the results of the one turn since
// are cut by what is over the limit, at the bytes a token takes in this
// context.
func TestCutResultsBytes(t *testing.T) {
	res := []session.Entry{
		{Kind: session.KindToolResult, Output: strings.Repeat("a", 30000)},
		{Kind: session.KindToolResult, Output: strings.Repeat("b", 10000)},
	}
	cutResults(res, 20000)
	if n := len(res[0].Output) + len(res[1].Output); n < 19000 || n > 21500 {
		t.Errorf("%d bytes left of 40000 cut by 20000", n)
	}
	if len(res[0].Output) < 2*len(res[1].Output) {
		t.Errorf("not in proportion: %d and %d", len(res[0].Output), len(res[1].Output))
	}
}

// The request for a summary is dated like any other: the model is not told
// it was made in the year 1.
func TestSummarizeTime(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "we talked"}}}
	a, j, _, _, cwd := newAgent(t, prov)
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "hi", Cwd: cwd, Time: time.Now()},
		{Kind: session.KindAssistant, Text: "hello"},
	}
	if err := a.Compact(context.Background(), "", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	msgs := prov.requests[0].Messages
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "reply with the summary only") {
		t.Fatalf("last message %+v", last)
	}
	if strings.Contains(last.Text, "0001-01-01") {
		t.Errorf("the summary is asked for with no time: %q", last.Text)
	}
}
