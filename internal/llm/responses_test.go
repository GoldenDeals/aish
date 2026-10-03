package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

func TestResponsesInput(t *testing.T) {
	p := newResponses(config.Config{Model: "m", APIKey: "k"})
	raw := `[
		{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"},
		{"id":"fc_3","type":"function_call","status":"completed","call_id":"c3","name":"bash","arguments":"{}"}
	]`
	got := p.input([]Message{
		{Role: RoleUser, Text: "list files"},
		// Another provider's encoding is rebuilt from the text and the calls.
		{Role: RoleAssistant, Text: "listing", ToolCalls: []ToolCall{
			{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
			{ID: "c2", Name: "read_file"},
		}, Raw: json.RawMessage(`{"role":"assistant","content":[]}`), Provider: "anthropic", Model: "m"},
		{Role: RoleUser, Text: "<shell>…</shell>", ToolResults: []ToolResult{
			{CallID: "c1", Content: "a b"},
			{CallID: "c2", Content: "no such file", IsError: true},
		}},
		// Its own goes as it came, reasoning and all.
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c3", Name: "bash", Args: json.RawMessage(`{}`)}},
			Raw: json.RawMessage(raw), Provider: "openai-responses", Model: "m"},
		{Role: RoleUser, ToolResults: []ToolResult{{CallID: "c3"}}},
	})
	sameJSON(t, got, `[
		{"role":"user","content":"list files"},
		{"role":"assistant","content":"listing"},
		{"type":"function_call","call_id":"c1","name":"bash","arguments":"{\"command\":\"ls\"}"},
		{"type":"function_call","call_id":"c2","name":"read_file","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"a b"},
		{"type":"function_call_output","call_id":"c2","output":"ERROR: no such file"},
		{"role":"user","content":"<shell>…</shell>"},
		{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"},
		{"id":"fc_3","type":"function_call","status":"completed","call_id":"c3","name":"bash","arguments":"{}"},
		{"type":"function_call_output","call_id":"c3","output":"(no output)"}
	]`)
}

func TestResponsesReplay(t *testing.T) {
	p := newResponses(config.Config{Model: "m", APIKey: "k"})
	raw := `[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}]}]`
	rebuilt := `[{"role":"assistant","content":"hi"}]`
	for _, tc := range []struct {
		name, raw, provider, model, want string
	}{
		{"same model", raw, "openai-responses", "m", raw},
		{"model not recorded", raw, "openai-responses", "", raw},
		// Encrypted reasoning is only for the model that made it.
		{"other model", raw, "openai-responses", "other", rebuilt},
		{"chat completions", raw, "openai", "m", rebuilt},
		{"broken raw", `[{"id":`, "openai-responses", "m", rebuilt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := p.input([]Message{{Role: RoleAssistant, Text: "hi", Raw: json.RawMessage(tc.raw), Provider: tc.provider, Model: tc.model}})
			sameJSON(t, got, tc.want)
		})
	}
}

// sse is a stream of Responses events, one JSON object a line.
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var head struct{ Type string }
		json.Unmarshal([]byte(e), &head)
		b.WriteString("event: " + head.Type + "\ndata: " + e + "\n\n")
	}
	return b.String()
}

var responsesStream = sse(
	`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m","output":[]}}`,
	`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"looking"}`,
	`{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":1,"content_index":0,"delta":" now"}`,
	`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[`+
		`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"},`+
		`{"id":"rs_2","type":"reasoning","summary":[]},`+
		`{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"looking now","annotations":[]}]},`+
		`{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}"},`+
		`{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_2","name":"read_file","arguments":"{\"path\":"}`+
		`],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":64},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":107}}}`,
)

func TestResponsesComplete(t *testing.T) {
	var path string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, responsesStream)
	}))
	defer srv.Close()

	p, err := New(config.Config{Provider: "openai-responses", Model: "m", APIKey: "k", Effort: "xhigh", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	resp, err := p.Complete(context.Background(), Request{
		System: "sys",
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
			{Role: RoleAssistant, Text: "hello", Provider: "openai-responses", Model: "m",
				Raw: json.RawMessage(`[{"id":"rs_0","type":"reasoning","summary":[],"encrypted_content":"enc0"}]`)},
			{Role: RoleUser, Text: "list"},
		},
		Tools: []ToolDef{{Name: "bash", Description: "run", Schema: map[string]any{
			"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}},
		}}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/responses" {
		t.Errorf("request to %s", path)
	}

	var sent struct {
		Stream          bool             `json:"stream"`
		Store           *bool            `json:"store"`
		Include         []string         `json:"include"`
		Instructions    string           `json:"instructions"`
		MaxOutputTokens int              `json:"max_output_tokens"`
		Reasoning       map[string]any   `json:"reasoning"`
		Input           []map[string]any `json:"input"`
		Tools           []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if !sent.Stream || sent.Store == nil || *sent.Store || !slices.Equal(sent.Include, []string{"reasoning.encrypted_content"}) {
		t.Errorf("not stateless with the reasoning kept: %s", body)
	}
	if sent.Instructions != "sys" || sent.MaxOutputTokens != 64000 || sent.Reasoning["effort"] != "xhigh" {
		t.Errorf("request: %s", body)
	}
	if len(sent.Input) != 3 || sent.Input[1]["encrypted_content"] != "enc0" || sent.Input[2]["content"] != "list" {
		t.Errorf("input: %s", body)
	}
	if len(sent.Tools) != 1 || sent.Tools[0]["type"] != "function" || sent.Tools[0]["name"] != "bash" || sent.Tools[0]["strict"] != false {
		t.Errorf("tools: %s", body)
	}

	if resp.Text != "looking now" || !slices.Equal(deltas, []string{"looking", " now"}) {
		t.Errorf("text %q, deltas %q", resp.Text, deltas)
	}
	if resp.StopReason != "completed" || resp.InputTokens != 100 || resp.CachedTokens != 64 || resp.OutputTokens != 7 {
		t.Errorf("stop %q, tokens in %d, cached %d, out %d", resp.StopReason, resp.InputTokens, resp.CachedTokens, resp.OutputTokens)
	}
	want := []ToolCall{
		{ID: "call_1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		{ID: "call_2", Name: "read_file", Args: json.RawMessage(`{}`)},
	}
	if len(resp.ToolCalls) != len(want) {
		t.Fatalf("tool calls %+v", resp.ToolCalls)
	}
	for i, c := range resp.ToolCalls {
		if c.ID != want[i].ID || c.Name != want[i].Name || string(c.Args) != string(want[i].Args) {
			t.Errorf("call %d: %s %s %s", i, c.ID, c.Name, c.Args)
		}
	}
	// What goes back next turn: the items as they came, but reasoning
	// without its content, which the API could not find.
	var items []struct{ ID string }
	if err := json.Unmarshal(resp.Raw, &items); err != nil {
		t.Fatalf("raw %s: %v", resp.Raw, err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if !slices.Equal(ids, []string{"rs_1", "msg_1", "fc_1", "fc_2"}) || !strings.Contains(string(resp.Raw), `"encrypted_content":"enc"`) {
		t.Errorf("raw %s", resp.Raw)
	}
}

func TestResponsesEnd(t *testing.T) {
	for _, tc := range []struct {
		name, event, stop, err string
	}{
		{"cut short", `{"type":"response.incomplete","sequence_number":1,"response":{"id":"r","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`, "max_tokens", ""},
		{"failed", `{"type":"response.failed","sequence_number":1,"response":{"id":"r","object":"response","status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}}`, "", "server_error: boom"},
		{"error", `{"type":"error","sequence_number":1,"code":"rate_limit_exceeded","message":"slow down","param":null}`, "", "rate_limit_exceeded: slow down"},
		{"no end", `{"type":"response.created","sequence_number":0,"response":{"id":"r","object":"response","status":"in_progress","output":[]}}`, "", "openai-responses: stream ended early: unexpected EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(tc.event))
			}))
			defer srv.Close()
			p := newResponses(config.Config{Model: "m", APIKey: "k", BaseURL: srv.URL})
			resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, nil)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("error %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || resp.StopReason != tc.stop || resp.Raw != nil {
				t.Errorf("%+v %v", resp, err)
			}
		})
	}
}
