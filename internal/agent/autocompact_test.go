package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// A long request on a 32k window does not run into the limit: past
// compact_at the session is summed up and the request goes on from the
// summary, with the instructions read again.
func TestAutoCompactInRequest(t *testing.T) {
	read := func(id string, in int) *llm.Response {
		return &llm.Response{ToolCalls: []llm.ToolCall{toolCall(id, "read_file", `{"path":"big.txt"}`)}, InputTokens: in, OutputTokens: 100}
	}
	prov := &fakeProvider{replies: []*llm.Response{
		read("c1", 6000),
		read("c2", 12000),
		read("c3", 18000),
		read("c4", 23000), // with its result the context is past 0.8 × 32k
		{Text: "read big.txt four times, it is the same"},
		{Text: "done"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow = 32000
	if err := os.WriteFile(filepath.Join(cwd, "big.txt"), []byte(strings.Repeat("line of output\n", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("be brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "read big.txt four times", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	want := "instructions user" + strings.Repeat(" assistant tool_result", 4) + " summary instructions assistant"
	if got := kinds(j.es); got != want {
		t.Fatalf("journal %s", got)
	}
	if len(prov.requests) != 6 {
		t.Fatalf("%d requests", len(prov.requests))
	}
	sum := prov.requests[4].Messages
	if last := sum[len(sum)-1].Text; !strings.Contains(last, "compacted automatically") {
		t.Errorf("summary asked for with %q", last)
	}
	next := prov.requests[5].Messages
	if len(next) != 1 || !strings.Contains(next[0].Text, "<summary>") || !strings.Contains(next[0].Text, "be brief") ||
		strings.Contains(next[0].Text, "line of output") {
		t.Errorf("after the summary the model got %+v", next)
	}
	out := ui.String()
	if !strings.Contains(out, "context at 89% of the window, compacting") || !strings.Contains(out, "compacted: 28k → ") {
		t.Errorf("terminal:\n%s", out)
	}
	if strings.Contains(out, "still past compact_at") {
		t.Errorf("the summary did not help:\n%s", out)
	}
}

// Past compact_at at the start of a request, what the user did since is
// summed up, and the request goes after the summary as typed.
func TestAutoCompactAtStart(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "the user built the project"}, {Text: "it built"}}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow = 32000
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("make builds it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "hi", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "hello", InputTokens: 24000},
		{Kind: session.KindShell, Cmd: "make", Output: strings.Repeat("x", 30000), Cwd: cwd},
	}
	if err := a.Start(context.Background(), "did @notes.txt help", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant shell file user summary file user assistant" {
		t.Fatalf("journal %s", got)
	}
	if typed, copied := j.es[4], j.es[7]; copied.Text != typed.Text || !copied.Time.Equal(typed.Time) {
		t.Errorf("the request after the summary %+v, typed %+v", copied, typed)
	}
	next := prov.requests[1].Messages
	if len(next) != 1 || !strings.Contains(next[0].Text, "the user built the project") ||
		!strings.Contains(next[0].Text, "make builds it") || !strings.Contains(next[0].Text, "did @notes.txt help") ||
		strings.Contains(next[0].Text, "xxx") {
		t.Errorf("after the summary the model got %+v", next)
	}
}

// Right after a summary another one would not help: the result that took
// the context past compact_at is cut for the model, the journal keeps it.
func TestAutoCompactNotTwice(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"cat log"}`)}, InputTokens: 24000},
		{Text: "done"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow = 32000
	j.es = []session.Entry{{Kind: session.KindSummary, Text: "earlier work", Cwd: cwd}}
	if err := a.Start(context.Background(), "show the log", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	sh.outputs["c1"] = rpc.Output{Output: strings.Repeat("log line\n", 1600), Cwd: cwd}
	if err := a.Resume(context.Background(), "c1", 0, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "summary user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	if n := len(j.es[3].Output); n < 14400 {
		t.Errorf("the journal got the result cut to %d bytes", n)
	}
	res := prov.requests[1].Messages[len(prov.requests[1].Messages)-1].ToolResults
	if len(res) != 1 || len(res[0].Content) > 8000 || !strings.Contains(res[0].Content, "bytes omitted") ||
		!strings.HasSuffix(res[0].Content, "[exit 0, cwd "+cwd+"]") {
		t.Errorf("the model got %+v", res)
	}
	if !strings.Contains(ui.String(), "the last output cut") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// compact_at = 0 or an unknown window leave the context alone; a summary
// that fails does not stop the request.
func TestAutoCompactOffOrFailing(t *testing.T) {
	history := []session.Entry{
		{Kind: session.KindUser, Text: "hi"},
		{Kind: session.KindAssistant, Text: "hello", InputTokens: 31000},
	}
	for _, tc := range []struct {
		name          string
		window        int
		at            float64
		replies       []*llm.Response
		journal, note string
	}{
		{"off", 32000, 0, []*llm.Response{{Text: "ok"}}, "user assistant user assistant", ""},
		{"no window", 0, 0.8, []*llm.Response{{Text: "ok"}}, "user assistant user assistant", ""},
		{"empty summary", 32000, 0.8, []*llm.Response{{Text: " "}, {Text: "ok"}}, "user assistant user assistant",
			"could not compact: the model returned an empty summary"},
	} {
		prov := &fakeProvider{replies: tc.replies}
		a, j, _, ui, cwd := newAgent(t, prov)
		a.Cfg.ContextWindow, a.Cfg.CompactAt = tc.window, tc.at
		j.es = append([]session.Entry(nil), history...)
		if err := a.Start(context.Background(), "again", tools.Exec{Dir: cwd}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := kinds(j.es); got != tc.journal || j.es[len(j.es)-1].Text != "ok" {
			t.Errorf("%s: journal %s", tc.name, got)
		}
		if out := ui.String(); tc.note != "" && !strings.Contains(out, tc.note) || tc.note == "" && strings.Contains(out, "compact") {
			t.Errorf("%s: terminal %q", tc.name, out)
		}
	}
}
