package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

func TestOpenAIMessages(t *testing.T) {
	p := newOpenAI(config.Config{Model: "m", APIKey: "k"})
	got := p.messages(Request{System: "be brief", Messages: []Message{
		{Role: RoleUser, Text: "list files"},
		// Chat Completions has no encoding of its own to replay.
		{Role: RoleAssistant, Text: "listing", ToolCalls: []ToolCall{
			{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
			{ID: "c2", Name: "read_file"},
		}, Raw: json.RawMessage(`{"role":"assistant","content":[]}`), Provider: "anthropic", Model: "m"},
		{Role: RoleUser, Text: "<shell>…</shell>", ToolResults: []ToolResult{
			{CallID: "c1", Content: "a b"},
			{CallID: "c2", Content: "no such file", IsError: true},
		}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c3", Name: "bash", Args: json.RawMessage(`{}`)}}},
		{Role: RoleUser, ToolResults: []ToolResult{{CallID: "c3"}}},
	}})
	sameJSON(t, got, `[
		{"role":"system","content":"be brief"},
		{"role":"user","content":"list files"},
		{"role":"assistant","content":"listing","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}},
			{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"c1","content":"a b"},
		{"role":"tool","tool_call_id":"c2","content":"ERROR: no such file"},
		{"role":"user","content":"<shell>…</shell>"},
		{"role":"assistant","tool_calls":[
			{"id":"c3","type":"function","function":{"name":"bash","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"c3","content":"(no output)"}
	]`)

	got = p.messages(Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}})
	sameJSON(t, got, `[{"role":"user","content":"hi"}]`)
}

const openaiStream = `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"looking"},"finish_reason":null}]}

data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":" now"},"finish_reason":null}]}

data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":"}}]},"finish_reason":null}]}

data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":null}]}

data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]},"finish_reason":null}]}

data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"total_tokens":107,"prompt_tokens_details":{"cached_tokens":64}}}

data: [DONE]

`

func TestOpenAIComplete(t *testing.T) {
	var path, auth string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, openaiStream)
	}))
	defer srv.Close()

	// cliproxyapi is given without /v1, and over plain HTTP.
	for _, base := range []string{srv.URL, srv.URL + "/v1/"} {
		p := newOpenAI(config.Config{Provider: "openai", Model: "m", APIKey: "k", Effort: "xhigh", BaseURL: base})
		var deltas []string
		resp, err := p.Complete(context.Background(), Request{
			System:   "sys",
			Messages: []Message{{Role: RoleUser, Text: "list"}},
			Tools: []ToolDef{{Name: "bash", Description: "run", Schema: map[string]any{
				"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"},
			}}},
		}, func(s string) { deltas = append(deltas, s) })
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if path != "/v1/chat/completions" || auth != "Bearer k" {
			t.Errorf("%s: request to %s with %q", base, path, auth)
		}

		var sent struct {
			Stream        bool `json:"stream"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
			MaxCompletionTokens int              `json:"max_completion_tokens"`
			ReasoningEffort     string           `json:"reasoning_effort"`
			Messages            []map[string]any `json:"messages"`
			Tools               []struct {
				Function struct {
					Name       string         `json:"name"`
					Parameters map[string]any `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		if !sent.Stream || !sent.StreamOptions.IncludeUsage || sent.MaxCompletionTokens != 64000 || sent.ReasoningEffort != "xhigh" {
			t.Errorf("request: %s", body)
		}
		if len(sent.Messages) != 2 || len(sent.Tools) != 1 || sent.Tools[0].Function.Name != "bash" || sent.Tools[0].Function.Parameters["required"] == nil {
			t.Errorf("messages or tools: %s", body)
		}

		if resp.Text != "looking now" || !slices.Equal(deltas, []string{"looking", " now"}) {
			t.Errorf("text %q, deltas %q", resp.Text, deltas)
		}
		if resp.StopReason != "tool_calls" || resp.InputTokens != 100 || resp.CachedTokens != 64 || resp.OutputTokens != 7 {
			t.Errorf("stop %q, tokens in %d, cached %d, out %d", resp.StopReason, resp.InputTokens, resp.CachedTokens, resp.OutputTokens)
		}
		// A call cut short leaves arguments that do not parse; the call still
		// needs a result, so it goes on with no arguments.
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
	}
}
