package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

const anthropicStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

func TestAnthropicCacheControl(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicStream)
	}))
	defer srv.Close()

	p := newAnthropic(config.Config{Provider: "anthropic", Model: "m", APIKey: "k", BaseURL: srv.URL})
	raw := `{"role":"assistant","content":[{"type":"text","text":"listing"},{"type":"tool_use","id":"c1","name":"bash","input":{"command":"ls"}}]}`
	req := Request{
		System: "system prompt",
		Tools: []ToolDef{
			{Name: "bash", Description: "run", Schema: map[string]any{"properties": map[string]any{}}},
			{Name: "read", Description: "read", Schema: map[string]any{"properties": map[string]any{}}},
		},
		Messages: []Message{
			{Role: RoleUser, Text: "list files"},
			{Role: RoleAssistant, Raw: json.RawMessage(raw), Provider: "anthropic", Model: "m"},
			{Role: RoleUser, ToolResults: []ToolResult{{CallID: "c1", Content: "a b"}}, Text: "<shell>…</shell>"},
		},
	}
	resp, err := p.Complete(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 330 || resp.CachedTokens != 300 || resp.OutputTokens != 5 {
		t.Errorf("tokens: in %d, cached %d, out %d; want 330, 300, 5", resp.InputTokens, resp.CachedTokens, resp.OutputTokens)
	}

	if n := strings.Count(string(body), `"cache_control"`); n != 3 {
		t.Errorf("%d cache_control in the request, want 3:\n%s", n, body)
	}
	var sent struct {
		Tools    []map[string]any `json:"tools"`
		System   []map[string]any `json:"system"`
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Tools[0]["cache_control"] != nil || sent.Tools[1]["cache_control"] == nil {
		t.Errorf("want a breakpoint on the last tool only: %v", sent.Tools)
	}
	if sent.System[0]["cache_control"] == nil {
		t.Errorf("no breakpoint on the system prompt: %v", sent.System)
	}
	last := sent.Messages[2].Content
	if !strings.Contains(string(last[len(last)-1]), `"cache_control"`) {
		t.Errorf("no breakpoint on the last block of the last user message: %s", last[len(last)-1])
	}
	for _, b := range sent.Messages[1].Content {
		if strings.Contains(string(b), `"cache_control"`) {
			t.Errorf("replayed assistant message got a breakpoint: %s", b)
		}
	}
}
