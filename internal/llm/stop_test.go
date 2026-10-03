package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

func anthropicStop(reason string) string {
	return strings.Replace(anthropicStream, `"stop_reason":"end_turn"`, `"stop_reason":"`+reason+`"`, 1)
}

func openaiStop(reason string) string {
	return `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}

data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}

data: [DONE]

`
}

func responsesStop(status, details string) string {
	return sse(`{"type":"response.` + status + `","sequence_number":1,"response":{"id":"r","object":"response","status":"` + status +
		`","incomplete_details":` + details + `,"output":[]}}`)
}

// The agent knows a reply cut short by the provider's reason mapped to its
// own; a reason it does not act on comes as the provider gave it.
func TestStopReason(t *testing.T) {
	for _, tc := range []struct {
		name, provider, stream, want string
	}{
		{"anthropic context window", "anthropic", anthropicStop("model_context_window_exceeded"), StopContextWindow},
		{"anthropic max_tokens", "anthropic", anthropicStop("max_tokens"), StopMaxTokens},
		{"anthropic refusal", "anthropic", anthropicStop("refusal"), "refusal"},
		{"openai length", "openai", openaiStop("length"), StopMaxTokens},
		{"openai stop", "openai", openaiStop("stop"), "stop"},
		{"responses max_output_tokens", "openai-responses", responsesStop("incomplete", `{"reason":"max_output_tokens"}`), StopMaxTokens},
		{"responses content_filter", "openai-responses", responsesStop("incomplete", `{"reason":"content_filter"}`), StopRefusal},
		{"responses completed", "openai-responses", responsesStop("completed", `null`), "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, tc.stream)
			}))
			defer srv.Close()
			p, err := New(config.Config{Provider: tc.provider, Model: "m", APIKey: "k", BaseURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StopReason != tc.want {
				t.Errorf("stop reason %q, want %q", resp.StopReason, tc.want)
			}
		})
	}
}
