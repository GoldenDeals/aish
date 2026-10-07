package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// streamOf is what Complete of provider makes of a stream that is body,
// and the text it streamed on the way.
func streamOf(t *testing.T, provider, body string) (*Response, string, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	defer srv.Close()
	p, err := New(config.Config{Provider: provider, Model: "m", APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, func(s string) {
		text.WriteString(s)
	})
	return resp, text.String(), err
}

var responsesHead = []string{
	`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m","output":[]}}`,
	`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"abc"}`,
}

// The Responses API tells of a failure in the middle of a stream by an
// event of its own, which the SDK does not take for an error: its code
// alone says whether a retry may help.
func TestResponsesErrorEvents(t *testing.T) {
	for _, c := range []struct {
		name, event string
		retry       bool
		short       string
	}{
		{"error server", `{"type":"error","sequence_number":2,"code":"server_error","message":"The server had an error while processing your request.","param":null}`,
			true, "server_error: The server had an error while processing your request."},
		{"error rejected", `{"type":"error","sequence_number":2,"code":"invalid_request_error","message":"Invalid value for 'input'.","param":"input"}`,
			false, "invalid_request_error: Invalid value for 'input'."},
		{"error rate limit", `{"type":"error","sequence_number":2,"code":"rate_limit_exceeded","message":"Rate limit reached\nfor gpt in organization","param":null}`,
			true, "rate_limit_exceeded: Rate limit reached"},
		{"failed server", `{"type":"response.failed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1,"status":"failed","model":"m","output":[],"error":{"code":"server_error","message":"An error occurred while processing your request."}}}`,
			true, "server_error: An error occurred while processing your request."},
		{"failed prompt", `{"type":"response.failed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1,"status":"failed","model":"m","output":[],"error":{"code":"invalid_prompt","message":"Invalid prompt: flagged."}}}`,
			false, "invalid_prompt: Invalid prompt: flagged."},
		{"failed bare", `{"type":"response.failed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1,"status":"failed","model":"m","output":[]}}`,
			false, "the response failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, text, err := streamOf(t, "openai-responses", sse(append(responsesHead, c.event)...))
			if err == nil {
				t.Fatalf("no error; reply %+v", resp)
			}
			if text != "abc" {
				t.Errorf("streamed %q before the error", text)
			}
			if got := Retryable(err); got != c.retry {
				t.Errorf("Retryable(%v) %v, want %v", err, got, c.retry)
			}
			if got := Short(err); got != c.short {
				t.Errorf("Short %q, want %q", got, c.short)
			}
		})
	}
}

// A stream closed before response.completed is the start of a reply, as an
// Anthropic one without message_stop: a retry may get the whole of it.
func TestResponsesStreamEndedEarly(t *testing.T) {
	resp, text, err := streamOf(t, "openai-responses", sse(responsesHead...))
	if err == nil {
		t.Fatalf("no error; reply %+v", resp)
	}
	if text != "abc" {
		t.Errorf("streamed %q before the end", text)
	}
	if !Retryable(err) {
		t.Errorf("not retryable: %v", err)
	}
}

const chatHead = `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"abc"},"finish_reason":null}]}

`

const (
	chatFinish = `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

`
	chatUsage = `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}

`
	chatDone = "data: [DONE]\n\n"
)

// The SDK ends a Chat Completions stream alike on [DONE] and on a
// connection closed early. Either a finish reason or [DONE] is the end of
// the reply; without both it was cut off.
func TestChatStreamEnds(t *testing.T) {
	for _, c := range []struct {
		name, body string
		whole      bool
	}{
		{"whole", chatHead + chatFinish + chatUsage + chatDone, true},
		{"no finish reason", chatHead + chatUsage + chatDone, true},
		{"no [DONE]", chatHead + chatFinish, true},
		{"[DONE] without a blank line", chatHead + chatUsage + "data: [DONE]", true},
		{"cut", chatHead, false},
		{"cut after usage", chatHead + chatUsage, false},
		{"nothing", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, text, err := streamOf(t, "openai", c.body)
			if !c.whole {
				if err == nil {
					t.Fatalf("no error; reply %+v", resp)
				}
				if !Retryable(err) {
					t.Errorf("not retryable: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if text != "abc" || resp.Text != "abc" {
				t.Errorf("streamed %q, reply %+v", text, resp)
			}
		})
	}
}
