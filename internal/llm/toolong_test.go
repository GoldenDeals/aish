package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// The API's refusal of a request too long for the window is told from its
// other errors as the SDK gives it, for each provider.
func TestPromptTooLong(t *testing.T) {
	const openaiTooLong = `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`
	for _, tc := range []struct {
		name, provider string
		status         int
		body           string
		want           bool
	}{
		{"anthropic too long", "anthropic", 400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, true},
		{"anthropic with max_tokens", "anthropic", 400,
			"{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"input length and `max_tokens` exceed context limit: 188240 + 21333 > 200000, decrease input length or `max_tokens` and try again\"}}", true},
		{"anthropic other 400", "anthropic", 400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content: tool_use ids must be unique"}}`, false},
		{"anthropic too large", "anthropic", 413,
			`{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`, false},
		{"openai too long", "openai", 400, openaiTooLong, true},
		{"openai other 400", "openai", 400,
			`{"error":{"message":"Invalid schema for function 'bash'","type":"invalid_request_error","param":"tools","code":"invalid_function_parameters"}}`, false},
		{"openai server without the code", "openai", 400,
			`{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens.","type":"BadRequestError","param":null,"code":400}}`, true},
		{"responses too long", "openai-responses", 400,
			`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","param":"input","code":"context_length_exceeded"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			p, err := New(config.Config{Provider: tc.provider, Model: "m", APIKey: "k", BaseURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, nil)
			if err == nil {
				t.Fatal("no error")
			}
			if got := PromptTooLong(err); got != tc.want {
				t.Errorf("PromptTooLong(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}
	for _, err := range []error{nil, errors.New("prompt is too long"), context.Canceled} {
		if PromptTooLong(err) {
			t.Errorf("PromptTooLong(%v)", err)
		}
	}
	if !PromptTooLong(streamError(openaiTooLong)) {
		t.Error("an error event in the stream is not told")
	}
	// The Responses API may tell it in an event of its own.
	for _, ev := range []string{
		`{"type":"response.failed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1,"status":"failed","model":"m","output":[],"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}`,
		`{"type":"error","sequence_number":2,"code":"context_length_exceeded","message":"Your input exceeds the context window of this model.","param":"input"}`,
	} {
		_, _, err := streamOf(t, "openai-responses", sse(append(responsesHead, ev)...))
		if !PromptTooLong(err) {
			t.Errorf("PromptTooLong(%v) for %s", err, ev)
		}
	}
	_, _, err := streamOf(t, "openai-responses", sse(append(responsesHead,
		`{"type":"error","sequence_number":2,"code":"server_error","message":"The server had an error.","param":null}`)...))
	if err == nil || PromptTooLong(err) {
		t.Errorf("PromptTooLong(%v)", err)
	}
}

// The model's refusal comes as one reason whatever the provider calls it.
func TestRefusalStop(t *testing.T) {
	for _, tc := range []struct{ provider, stream string }{
		{"anthropic", anthropicStop("refusal")},
		{"openai", openaiStop("content_filter")},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, tc.stream)
		}))
		p, err := New(config.Config{Provider: tc.provider, Model: "m", APIKey: "k", BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, nil)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StopReason != StopRefusal {
			t.Errorf("%s: stop reason %q, want %q", tc.provider, resp.StopReason, StopRefusal)
		}
	}
}
