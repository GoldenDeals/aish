package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// What aish context shows is what the next request sends: past the last
// summary, masked, truncated, without the Raw of another profile.
func TestContextMessages(t *testing.T) {
	cfg := config.Default()
	cfg.MaxOutputBytes = 1000
	cfg.MaskDefaults = true
	cfg.Mask = []string{`hunter2-\w+`}
	cfg.Profile = "personal"
	secret := "ghp_" + strings.Repeat("a", 36)
	call := session.ToolCall{ID: "c1", Name: "read_file", Args: json.RawMessage(`{"path":"notes"}`)}
	es := []session.Entry{
		{Kind: session.KindShell, Cmd: "echo before", Output: "before the summary\n", Cwd: "/w"},
		{Kind: session.KindUser, Text: "old question", Cwd: "/w"},
		{Kind: session.KindAssistant, Text: "old answer"},
		{Kind: session.KindSummary, Text: "we did things"},
		{Kind: session.KindShell, Cmd: "cat token", Output: secret + "\n", Cwd: "/w"},
		{Kind: session.KindShell, Cmd: "cat big", Output: strings.Repeat("x", 5000), Cwd: "/w"},
		{Kind: session.KindUser, Text: "what is in them", Cwd: "/w"},
		{Kind: session.KindAssistant, Text: "let me look", Raw: json.RawMessage(`{"x":1}`), Provider: "fake", Model: "m",
			Profile: "work", ToolCalls: []session.ToolCall{call}},
		{Kind: session.KindToolResult, ToolCallID: "c1", ToolName: "read_file", Output: "pass hunter2-abc, key " + secret},
		{Kind: session.KindAssistant, Text: "done", Raw: json.RawMessage(`{"y":2}`), Provider: "fake", Model: "m", Profile: "personal"},
	}
	msgs, err := ContextMessages(es, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("%d messages: %+v", len(msgs), msgs)
	}
	first := msgs[0].Text
	if strings.Contains(first, "before the summary") || strings.Contains(first, "old question") {
		t.Errorf("entries before the summary sent:\n%s", first)
	}
	// The commands and the request after the summary are one user message.
	for _, s := range []string{"<summary>", "$ cat token", "$ cat big", "what is in them"} {
		if !strings.Contains(first, s) {
			t.Errorf("no %q in the first message:\n%s", s, first)
		}
	}
	if !strings.Contains(first, "bytes omitted") || strings.Contains(first, strings.Repeat("x", 1001)) {
		t.Errorf("output longer than max_output not truncated:\n%s", first)
	}
	for i, m := range msgs {
		text := m.Text
		for _, r := range m.ToolResults {
			text += r.Content
		}
		if strings.Contains(text, secret) || strings.Contains(text, "hunter2-abc") {
			t.Errorf("message %d has a secret: %q", i, text)
		}
	}
	if r := msgs[2].ToolResults; len(r) != 1 || !strings.Contains(r[0].Content, "ghp_***") {
		t.Errorf("tool results %+v", r)
	}
	if msgs[1].Raw != nil || msgs[1].Text != "let me look" || len(msgs[1].ToolCalls) != 1 {
		t.Errorf("reply of another profile %+v", msgs[1])
	}
	if string(msgs[3].Raw) != `{"y":2}` {
		t.Errorf("reply of this profile without Raw: %+v", msgs[3])
	}
	if len(es[7].Raw) == 0 || !strings.Contains(es[4].Output, secret) {
		t.Errorf("the journal itself changed")
	}

	// The point: the same as what a request of the agent sends.
	a := &Agent{Cfg: cfg, Tools: &tools.Registry{}, env: "-"}
	if want := a.request(es).Messages; !reflect.DeepEqual(msgs, want) {
		t.Errorf("context differs from the request:\n%+v\nwant\n%+v", msgs, want)
	}

	cfg.Mask = []string{"("}
	if _, err := ContextMessages(es, cfg); err == nil {
		t.Error("a bad mask pattern accepted")
	}
}
