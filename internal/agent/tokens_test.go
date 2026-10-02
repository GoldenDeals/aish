package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
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
	if over < len(a.Cfg.SystemPrompt)/4 {
		t.Fatalf("overhead %d", over)
	}
	if got, want := a.contextTokens(es), session.Tokens(es, a.Cfg.MaxOutputBytes)+over; got != want {
		t.Errorf("after a summary %d, want %d", got, want)
	}

	es = append(es, session.Entry{Kind: session.KindAssistant, Text: "now this", InputTokens: 9000, OutputTokens: 50})
	if got, want := a.contextTokens(es), session.Tokens(es, a.Cfg.MaxOutputBytes); got != want {
		t.Errorf("after a measured turn %d, want %d", got, want)
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
