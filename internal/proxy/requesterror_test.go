package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// The error a request ends with is what the shell prints: the API's own
// type and message in a line, not the SDK's URL, request ID and body.
func TestRequestAPIError(t *testing.T) {
	const message = "Invalid 'messages[1].content': string too long. Expected a string with maximum length 10485760, but got a string with length 12000000 instead."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req_011CXYZ")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"`+message+`","type":"invalid_request_error","param":"messages[1].content","code":"string_above_max_length"}}`)
	}))
	defer srv.Close()

	p, _, cwd := hosted(t, &scripted{})
	p.window = 128000 // no models list asked for
	p.newProvider = func(cfg config.Config) (llm.Provider, error) {
		cfg.Provider, cfg.APIKey, cfg.BaseURL = "openai", "k", srv.URL
		return llm.New(cfg)
	}
	_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hi", Cwd: cwd})
	if err == nil {
		t.Fatal("no error")
	}
	if want := "string_above_max_length: " + message; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if s := err.Error(); strings.Contains(s, "\n") || strings.Contains(s, srv.URL) || strings.Contains(s, "req_011CXYZ") {
		t.Errorf("more than the API's error: %q", s)
	}
}
