package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
)

// recapJournal is a session with a summary and a clear in it: a request
// before the summary, one between it and the clear, one after the clear,
// with secrets in the output of a tool and of a command.
func recapJournal(cwd string) []session.Entry {
	return []session.Entry{
		{Kind: session.KindUser, Text: "find the old logs", Cwd: cwd},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{{ID: "c1", Name: "bash", Args: []byte(`{"command":"env"}`)}}},
		{Kind: session.KindToolResult, ToolCallID: "c1", ToolName: "bash", Output: "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\n"},
		{Kind: session.KindAssistant, Text: "they are in /var/log/old"},
		{Kind: session.KindSummary, Text: "The user looked for old logs.", Cwd: cwd},
		{Kind: session.KindUser, Text: "remove them", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "removed"},
		{Kind: session.KindClear},
		{Kind: session.KindShell, Cmd: "cat .env", Output: "OPENAI=sk-abcdefghijklmnopqrstuvwxyz0123\n", Cwd: cwd},
		{Kind: session.KindUser, Text: "what is in .env", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "a key"},
	}
}

// The recap is asked for the whole journal, before the summary and after
// the clear, with the secrets the model never sees masked; it is shown,
// and nothing is recorded.
func TestRecapWholeSession(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "You looked for old logs and removed them.", InputTokens: 900}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	j.es = recapJournal(cwd)
	a.load(true)
	entries := append([]session.Entry(nil), a.entries...)
	n := len(j.es)

	if err := a.Recap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("%d requests", len(prov.requests))
	}
	req := prov.requests[0]
	if len(req.Messages) != 1 || req.Messages[0].Role != llm.RoleUser || len(req.Tools) != 0 {
		t.Fatalf("request %+v", req)
	}
	text := req.Messages[0].Text
	for _, want := range []string{
		"find the old logs", "they are in /var/log/old", `<tool_call name="bash">`, "remove them", "what is in .env",
		"$ cat .env", compactedMark, clearMark, "AKIA***", "sk-a***", "Reply with the recap only",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in the transcript:\n%s", want, text)
		}
	}
	for _, secret := range []string{"AKIAIOSFODNN7EXAMPLE", "sk-abcdefghijklmnopqrstuvwxyz0123"} {
		if strings.Contains(text, secret) {
			t.Errorf("the secret %s is sent", secret)
		}
	}
	// The part before the summary is there whole: the summary would only
	// say it again.
	if strings.Contains(text, "The user looked for old logs.") {
		t.Errorf("the summary is sent along with what it sums up:\n%s", text)
	}
	if i, k := strings.Index(text, "find the old logs"), strings.Index(text, "what is in .env"); i > k {
		t.Errorf("the transcript is out of order:\n%s", text)
	}
	if len(j.es) != n {
		t.Errorf("the journal has %d entries, had %d: %s", len(j.es), n, kinds(j.es))
	}
	if !reflect.DeepEqual(a.entries, entries) {
		t.Errorf("the agent's entries changed: %s", kinds(a.entries))
	}
	if !strings.Contains(ui.String(), "You looked for old logs and removed them.") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// An empty session is not sent: there is nothing to retell.
func TestRecapEmpty(t *testing.T) {
	prov := &fakeProvider{}
	a, _, _, _, _ := newAgent(t, prov)
	if err := a.Recap(context.Background()); err == nil || err.Error() != "nothing to recap" {
		t.Errorf("recap of an empty session: %v", err)
	}
	if len(prov.requests) != 0 {
		t.Errorf("%d requests", len(prov.requests))
	}
}

// What a summary was made of gives way to the summary, the latest one of
// those after a clear, when the whole session does not fit the window.
func TestRecapPiecesBySummaries(t *testing.T) {
	a, _, _, _, cwd := newAgent(t, &fakeProvider{})
	es := []session.Entry{
		{Kind: session.KindUser, Text: "first", Cwd: cwd},
		{Kind: session.KindClear},
		{Kind: session.KindUser, Text: "second", Cwd: cwd},
		{Kind: session.KindSummary, Text: "sum A"},
		{Kind: session.KindUser, Text: "third", Cwd: cwd},
		{Kind: session.KindSummary, Text: "sum B"},
		{Kind: session.KindUser, Text: "fourth", Cwd: cwd},
	}
	got := strings.Join(a.recapPieces(recapParts(es), true, nil), "\n")
	for _, want := range []string{"first", clearMark, "<summary>\nsum B\n</summary>", "fourth"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, gone := range []string{"second", "third", "sum A"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is left in:\n%s", gone, got)
		}
	}
	if i, k := strings.Index(got, clearMark), strings.Index(got, "sum B"); i > k {
		t.Errorf("the summary made after the clear goes before it:\n%s", got)
	}
	whole := strings.Join(a.recapPieces(recapParts(es), false, nil), "\n")
	if strings.Count(whole, compactedMark) != 2 || strings.Contains(whole, "<summary>") || !strings.Contains(whole, "third") {
		t.Errorf("the whole transcript:\n%s", whole)
	}
}

// A session past the window is recapped from its summaries.
func TestRecapBySummaries(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "the recap"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow = 3000
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "early " + strings.Repeat("x", 20000), Cwd: cwd},
		{Kind: session.KindAssistant, Text: "done early"},
		{Kind: session.KindSummary, Text: "The user did the early work."},
		{Kind: session.KindUser, Text: "late request", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "done late"},
	}
	if err := a.Recap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("%d requests", len(prov.requests))
	}
	text := prov.requests[0].Messages[0].Text
	if strings.Contains(text, "early x") || !strings.Contains(text, "The user did the early work.") || !strings.Contains(text, "late request") {
		t.Errorf("transcript:\n%s", text)
	}
	if !strings.Contains(ui.String(), "go by their summaries") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// recapProvider answers a request to sum up a part with a summary of it
// and a request for the recap with the recap; the first recap fails as
// too long, if tooLong.
type recapProvider struct {
	fakeProvider
	tooLong bool
	sums    int
}

func (p *recapProvider) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	p.requests = append(p.requests, req)
	text := req.Messages[0].Text
	if strings.HasSuffix(text, condensePrompt) {
		p.sums++
		return &llm.Response{Text: "part summed up"}, nil
	}
	if p.tooLong {
		p.tooLong = false
		return nil, errTooLong()
	}
	if onText != nil {
		onText("the recap")
	}
	return &llm.Response{Text: "the recap"}, nil
}

// requestsAbout is a session of n requests, each about a kilotoken.
func requestsAbout(n int, cwd string) []session.Entry {
	var es []session.Entry
	for i := range n {
		es = append(es,
			session.Entry{Kind: session.KindUser, Text: "request " + string(rune('a'+i)) + " " + strings.Repeat("y", 4000), Cwd: cwd},
			session.Entry{Kind: session.KindAssistant, Text: "answer " + string(rune('a'+i))})
	}
	return es
}

// A session past the window with no summary to stand for its earliest
// part has that part summed up first, by calls of their own.
func TestRecapCondenses(t *testing.T) {
	prov := &recapProvider{}
	a, j, _, ui, cwd := newAgent(t, &prov.fakeProvider)
	a.Provider = prov
	a.Cfg.ContextWindow = 3000
	j.es = requestsAbout(4, cwd)
	n := len(j.es)
	if err := a.Recap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prov.sums == 0 || len(prov.requests) != prov.sums+1 {
		t.Fatalf("%d requests, %d of them summing up", len(prov.requests), prov.sums)
	}
	if first := prov.requests[0].Messages[0].Text; !strings.Contains(first, "request a ") {
		t.Errorf("the first part summed up:\n%s", first)
	}
	text := prov.requests[len(prov.requests)-1].Messages[0].Text
	if strings.Contains(text, "request a ") || !strings.Contains(text, "part summed up") || !strings.Contains(text, "request d ") {
		t.Errorf("transcript:\n%s", text)
	}
	if got := recapTokens([]string{text}); got > a.recapBudget()+len(recapPrompt)/4+10 {
		t.Errorf("the transcript is ~%d tokens, past %d", got, a.recapBudget())
	}
	if out := ui.String(); !strings.Contains(out, "summed up first") || !strings.Contains(out, "the recap") {
		t.Errorf("terminal:\n%s", out)
	}
	if len(j.es) != n {
		t.Errorf("the journal has %d entries, had %d", len(j.es), n)
	}
}

// With the window unknown, the API tells the transcript does not fit: it
// is sent again smaller.
func TestRecapTooLong(t *testing.T) {
	prov := &recapProvider{tooLong: true}
	a, j, _, ui, cwd := newAgent(t, &prov.fakeProvider)
	a.Provider = prov
	a.Cfg.ContextWindow = 0
	j.es = requestsAbout(4, cwd)
	if err := a.Recap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prov.sums == 0 || len(prov.requests) != prov.sums+2 {
		t.Fatalf("%d requests, %d of them summing up", len(prov.requests), prov.sums)
	}
	if !strings.Contains(prov.requests[0].Messages[0].Text, "request a ") {
		t.Errorf("the first request is not the whole session")
	}
	if !strings.Contains(ui.String(), "the recap") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// Ctrl+C stops the recap; nothing is recorded.
func TestRecapCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &fakeProvider{replies: []*llm.Response{{Text: "never"}}, before: func(context.Context, int, func(string)) { cancel() }}
	a, j, _, _, cwd := newAgent(t, prov)
	j.es = recapJournal(cwd)
	n := len(j.es)
	if err := a.Recap(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("recap stopped: %v", err)
	}
	if len(prov.requests) != 1 || len(j.es) != n {
		t.Errorf("%d requests, %d entries of %d", len(prov.requests), len(j.es), n)
	}
}
