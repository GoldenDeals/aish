package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/tools"
)

// A reply the context window cut is said to be cut, and the next request
// starts from a summary, though the estimate is far below compact_at and
// the window is not known. One summary frees the window: the request after
// it goes on as usual.
func TestWindowFullCompacts(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: "the answer is", StopReason: llm.StopContextWindow, InputTokens: 1000, OutputTokens: 3},
		{Text: "the user asked something"},
		{Text: "it is 42"},
		{Text: "bye"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow, a.Cfg.CompactAt = 0, 0.8
	if err := a.Start(context.Background(), "what is the answer", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant" || j.es[1].Text != "the answer is" {
		t.Fatalf("journal %s: %+v", got, j.es)
	}
	if out := ui.String(); !strings.Contains(out, "[aish: reply cut: the context window is full]") || strings.Contains(out, "aish compact") {
		t.Errorf("terminal:\n%s", out)
	}

	ui.Reset()
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant user summary user assistant" {
		t.Fatalf("journal %s", got)
	}
	if sum := prov.requests[1].Messages; !strings.Contains(sum[len(sum)-1].Text, "compacted automatically") {
		t.Errorf("summary asked for with %+v", sum[len(sum)-1])
	}
	if next := prov.requests[2].Messages; len(next) != 1 || !strings.Contains(next[0].Text, "<summary>") || !strings.Contains(next[0].Text, "go on") {
		t.Errorf("after the summary the model got %+v", next)
	}
	out := ui.String()
	if !strings.Contains(out, "the context window is full, compacting") || !strings.Contains(out, "compacted: ") ||
		strings.Contains(out, "still past compact_at") {
		t.Errorf("terminal:\n%s", out)
	}

	ui.Reset()
	if err := a.Start(context.Background(), "thanks", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant user summary user assistant user assistant" || len(prov.requests) != 4 {
		t.Errorf("journal %s after %d requests", got, len(prov.requests))
	}
	if strings.Contains(ui.String(), "compact") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// With compact_at = 0 nothing is summed up: the user is told how to free
// the window.
func TestWindowFullCompactOff(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: "the answer is", StopReason: llm.StopContextWindow, InputTokens: 1000},
		{Text: "ok"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow, a.Cfg.CompactAt = 200000, 0
	if err := a.Start(context.Background(), "what is the answer", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if out := ui.String(); !strings.Contains(out, "[aish: reply cut: the context window is full; aish compact frees it]") {
		t.Errorf("terminal:\n%s", out)
	}
	ui.Reset()
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant user assistant" || len(prov.requests) != 2 {
		t.Errorf("journal %s after %d requests", got, len(prov.requests))
	}
	if strings.Contains(ui.String(), "compact") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// The calls of a reply the window cut still run; a summary that fails is
// tried again before the turn after, till one is made.
func TestWindowFullSummaryFails(t *testing.T) {
	read := func(id string) llm.ToolCall { return toolCall(id, "read_file", `{"path":"notes.txt"}`) }
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: "reading", ToolCalls: []llm.ToolCall{read("c1")}, StopReason: llm.StopContextWindow, InputTokens: 1000},
		{Text: " "}, // an empty summary is an error
		{ToolCalls: []llm.ToolCall{read("c2")}, InputTokens: 1100},
		{Text: "read notes.txt twice"},
		{Text: "done"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow, a.Cfg.CompactAt = 0, 0.8
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("make builds it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "read notes.txt", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant tool_result summary assistant" {
		t.Fatalf("journal %s", got)
	}
	if len(prov.requests) != 5 {
		t.Fatalf("%d requests", len(prov.requests))
	}
	for _, i := range []int{1, 3} {
		if m := prov.requests[i].Messages; !strings.Contains(m[len(m)-1].Text, "compacted automatically") {
			t.Errorf("request %d is not for a summary: %+v", i, m[len(m)-1])
		}
	}
	out := ui.String()
	if strings.Count(out, "the context window is full, compacting") != 2 ||
		!strings.Contains(out, "could not compact: the model returned an empty summary") || !strings.Contains(out, "compacted: ") {
		t.Errorf("terminal:\n%s", out)
	}
}

// A summary made since the window was full, by `aish compact`, freed it:
// the next request does not sum the session up again.
func TestWindowFullCompactedByHand(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: "the answer is", StopReason: llm.StopContextWindow, InputTokens: 1000},
		{Text: "the user asked something"},
		{Text: "ok"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow, a.Cfg.CompactAt = 0, 0.8
	if err := a.Start(context.Background(), "what is the answer", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if err := a.Compact(context.Background(), "", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	ui.Reset()
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant summary user assistant" || len(prov.requests) != 3 {
		t.Errorf("journal %s after %d requests", got, len(prov.requests))
	}
	if strings.Contains(ui.String(), "compact") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}
