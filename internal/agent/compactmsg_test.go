package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// errSDKOverloaded is an API error as the SDK gives it: its text has the URL
// and the whole body.
func errSDKOverloaded() error {
	e := &anthropic.Error{}
	_ = e.UnmarshalJSON([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011"}`))
	e.StatusCode = 529
	e.Request = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	e.Response = &http.Response{StatusCode: 529}
	return e
}

// pastLimit is a history past compact_at of a 32k window.
func pastLimit(a *Agent, j *fakeJournal, cwd string) {
	a.Cfg.ContextWindow, a.Cfg.CompactAt = 32000, 0.8
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "hi", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "hello", InputTokens: 31000},
	}
}

// A turn the API rejects as too long is said to be compacted once: the
// summary that follows says the rest.
func TestTooLongOneCompactingLine(t *testing.T) {
	a, _, prov, ui, cwd := newRejected(t, map[int]error{0: errTooLong()},
		nil, &llm.Response{Text: "the user looked around"}, &llm.Response{Text: "it is 42"})
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 3 || !summaryRequest(prov.requests[1]) {
		t.Fatalf("%d requests", len(prov.requests))
	}
	if out := ui.String(); strings.Count(out, "compacting") != 1 || !strings.Contains(out, "the context window is full, compacting") ||
		!strings.Contains(out, "compacted: ") {
		t.Errorf("terminal:\n%s", out)
	}
}

// A summary the API failed is told by the error's short text, not the
// SDK's with the URL and the body.
func TestCompactErrorShort(t *testing.T) {
	sdk := errSDKOverloaded()
	a, j, prov, ui, cwd := newRejected(t, map[int]error{0: sdk}, nil, &llm.Response{Text: "ok"})
	pastLimit(a, j, cwd)
	if err := a.Start(context.Background(), "again", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 2 || !summaryRequest(prov.requests[0]) {
		t.Fatalf("%d requests", len(prov.requests))
	}
	out := ui.String()
	if !strings.Contains(out, "[aish: could not compact: "+llm.Short(sdk)+";") || strings.Contains(out, "api.anthropic.com") ||
		strings.Contains(out, "req_011") {
		t.Errorf("terminal:\n%s", out)
	}
}

// A summary that failed past compact_at is not asked for again before
// every turn of the request, a command handed to the shell between them
// too; the next request tries it again.
func TestThresholdSummaryFailsOnce(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: " "}, // an empty summary is an error
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"make"}`)}, InputTokens: 31500},
		{Text: "built"},
		{Text: "the user built the project"},
		{Text: "ok"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	pastLimit(a, j, cwd)
	if err := a.Start(context.Background(), "build it", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	sh.outputs["c1"] = rpc.Output{Output: "built\n", Cwd: cwd}
	if err := a.Resume(context.Background(), "c1", 0, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 3 || !summaryRequest(prov.requests[0]) || summaryRequest(prov.requests[1]) || summaryRequest(prov.requests[2]) {
		t.Fatalf("%d requests", len(prov.requests))
	}
	if got := kinds(j.es); got != "user assistant user assistant tool_result assistant" {
		t.Errorf("journal %s", got)
	}
	out := ui.String()
	if strings.Count(out, "compacting") != 1 || strings.Count(out, "could not compact") != 1 {
		t.Errorf("terminal:\n%s", out)
	}

	ui.Reset()
	if err := a.Start(context.Background(), "thanks", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 5 || !summaryRequest(prov.requests[3]) {
		t.Fatalf("%d requests", len(prov.requests))
	}
	if !strings.Contains(ui.String(), "compacted: ") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// A summary that failed past compact_at does not keep one off when the
// window is full: that goes as before.
func TestThresholdFailedWindowFull(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: " "},
		{ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"notes.txt"}`)}, StopReason: llm.StopContextWindow, InputTokens: 31500},
		{Text: "the user asked to read notes.txt"},
		{Text: "done"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	pastLimit(a, j, cwd)
	if err := a.Start(context.Background(), "read notes.txt", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 4 || !summaryRequest(prov.requests[0]) || !summaryRequest(prov.requests[2]) {
		t.Fatalf("%d requests", len(prov.requests))
	}
	if out := ui.String(); !strings.Contains(out, "the context window is full, compacting") || !strings.Contains(out, "compacted: ") {
		t.Errorf("terminal:\n%s", out)
	}
}
