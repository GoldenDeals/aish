package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/GoldenDeals/aish/internal/config"
)

// apiFailure is the error Complete of provider gets from a server that
// answers with status and body.
func apiFailure(t *testing.T, provider string, status int, contentType, body string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("request-id", "req_011CXYZ")
		w.Header().Set("x-request-id", "req_011CXYZ")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	cfg := config.Config{Provider: provider, Model: "m", APIKey: "k", BaseURL: srv.URL}
	var p Provider = newAnthropic(cfg)
	if provider == "openai" {
		p = newOpenAI(cfg)
	}
	_, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, nil)
	if err == nil {
		t.Fatalf("%s %d: no error", provider, status)
	}
	return err
}

func TestShortAPIErrors(t *testing.T) {
	for _, c := range []struct {
		name     string
		provider string
		status   int
		body     string
		want     string
	}{
		// An error event in a stream that began well: status 200.
		{"anthropic stream", "anthropic", 200, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"},\"request_id\":\"req_011CXYZ\"}\n\n",
			"overloaded_error: Overloaded"},
		{"anthropic rejected", "anthropic", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"},"request_id":"req_011CXYZ"}`,
			"invalid_request_error: prompt is too long: 210000 tokens > 200000 maximum"},
		{"openai rejected", "openai", 404, `{"error":{"message":"The model ` + "`m`" + ` does not exist or you do not have access to it.","type":"invalid_request_error","param":null,"code":"model_not_found"}}`,
			"model_not_found: The model `m` does not exist or you do not have access to it."},
		{"openai stream", "openai", 200, "data: {\"error\":{\"message\":\"The server had an error while processing your request.\",\"type\":\"server_error\",\"param\":null,\"code\":null}}\n\n",
			"server_error: The server had an error while processing your request."},
	} {
		contentType := "application/json"
		if c.status == 200 {
			contentType = "text/event-stream"
		}
		err := apiFailure(t, c.provider, c.status, contentType, c.body)
		got := Short(err)
		if got != c.want {
			t.Errorf("%s: Short %q, want %q\nerror: %v", c.name, got, c.want, err)
		}
		if strings.Contains(got, "http") || strings.Contains(got, "req_011CXYZ") {
			t.Errorf("%s: URL or request ID left in %q", c.name, got)
		}
	}
}

func TestShort(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("unknown tool"), "unknown tool"},
		{"lines", fmt.Errorf("read: %w", errors.New("broken\nmore detail")), "read: broken"},
		{"no body", &anthropic.Error{StatusCode: 502}, "502 Bad Gateway"},
		{"wrapped event", fmt.Errorf("turn: %w", streamError(`{"error":{"code":502,"message":"Upstream failed\nretry later"}}`)), "502: Upstream failed"},
		{"event string", streamError(`{"error":"something broke"}`), "something broke"},
		{"event type only", streamError(`{"error":{"type":"server_error"}}`), "server_error"},
		{"event unknown", streamError(`{"error":{}}`), "received error while streaming"},
		{"ended early", fmt.Errorf("anthropic: stream ended early: %w", io.ErrUnexpectedEOF), "anthropic: stream ended early: unexpected EOF"},
	} {
		if got := Short(c.err); got != c.want {
			t.Errorf("%s: Short %q, want %q", c.name, got, c.want)
		}
	}
}
