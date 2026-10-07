package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

const anthropicEmptyStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`

// An assistant message with no content is a 400, so an empty reply must not
// come back as Raw, nor be replayed from a journal that already keeps one.
func TestAnthropicEmptyReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicEmptyStream)
	}))
	defer srv.Close()

	p := newAnthropic(config.Config{Provider: "anthropic", Model: "m", APIKey: "k", BaseURL: srv.URL})
	resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "end_turn" || resp.Text != "" || len(resp.ToolCalls) != 0 {
		t.Errorf("reply: %+v", resp)
	}
	if resp.Raw != nil {
		t.Errorf("Raw of an empty reply: %s", resp.Raw)
	}

	got := p.messages([]Message{
		{Role: RoleUser, Text: "hi"},
		{Role: RoleAssistant, Raw: json.RawMessage(`{"role":"assistant","content":[]}`), Provider: "anthropic", Model: "m"},
		{Role: RoleUser, Text: "again"},
	})
	sameJSON(t, got, `[
		{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[{"type":"text","text":"(no response)"}]},
		{"role":"user","content":[{"type":"text","text":"again"}]}
	]`)
}
