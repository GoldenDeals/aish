package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/inebotov/aish/internal/config"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestRetryable(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"anthropic 529", &anthropic.Error{StatusCode: 529}, true},
		{"anthropic 500", &anthropic.Error{StatusCode: 500}, true},
		{"anthropic 429", &anthropic.Error{StatusCode: 429}, true},
		{"anthropic 400", &anthropic.Error{StatusCode: 400}, false},
		{"anthropic 401", &anthropic.Error{StatusCode: 401}, false},
		{"openai 503", &openai.Error{StatusCode: 503}, true},
		{"openai 413", &openai.Error{StatusCode: 413}, false},
		{"wrapped", fmt.Errorf("turn: %w", &anthropic.Error{StatusCode: 502}), true},
		{"unexpected EOF", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true},
		{"reset", &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, true},
		{"timeout", &net.OpError{Op: "read", Err: timeoutError{}}, true},
		{"canceled", context.Canceled, false},
		{"deadline", fmt.Errorf("post: %w", context.DeadlineExceeded), false},
		{"other", errors.New("unknown tool"), false},
		{"stream server_error", streamError(`{"error":{"message":"The server had an error","type":"server_error","param":null,"code":null}}`), true},
		{"stream rate limit", streamError(`{"error":{"message":"Rate limit reached","type":"tokens","code":"rate_limit_exceeded"}}`), true},
		{"stream responses", streamError(`{"type":"error","sequence_number":3,"error":{"type":"server_error","code":"server_error","message":"An error occurred"}}`), true},
		{"stream status code", streamError(`{"error":{"code":502,"message":"Bad gateway"}}`), true},
		{"stream wrapped", fmt.Errorf("turn: %w", streamError(`{"error":{"type":"overloaded","message":"busy"}}`)), true},
		{"stream invalid", streamError(`{"error":{"message":"Invalid schema","type":"invalid_request_error","param":"tools","code":null}}`), false},
		{"stream quota", streamError(`{"error":{"message":"You exceeded your quota","type":"insufficient_quota","code":"insufficient_quota"}}`), false},
		{"stream bare", streamError(`{"error":"something broke"}`), false},
		{"nil", nil, false},
	} {
		if got := Retryable(c.err); got != c.want {
			t.Errorf("%s: Retryable %v, want %v", c.name, got, c.want)
		}
	}
}

// An overloaded API may break a stream that has begun: the error event
// comes with status 200, and only its type says it is worth a retry.
func TestRetryableStreamError(t *testing.T) {
	const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"abc"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, stream)
	}))
	defer srv.Close()

	p := newAnthropic(config.Config{Provider: "anthropic", Model: "m", APIKey: "k", BaseURL: srv.URL})
	var text strings.Builder
	_, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, func(s string) {
		text.WriteString(s)
	})
	if err == nil {
		t.Fatal("no error from a broken stream")
	}
	if text.String() != "abc" {
		t.Errorf("streamed %q before the error", text.String())
	}
	if !Retryable(err) {
		t.Errorf("not retryable: %v", err)
	}
}

func streamError(data string) error {
	return &ssestream.StreamError{Message: "received error while streaming", Event: ssestream.Event{Data: []byte(data)}}
}

// An error event in the middle of a Chat Completions stream reaches
// Complete as the SDK's StreamError, not as an openai.Error: only the
// event tells whether a retry may help.
func TestRetryableOpenAIStream(t *testing.T) {
	const head = `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"abc"},"finish_reason":null}]}

`
	for _, c := range []struct {
		event string
		want  bool
	}{
		{`{"error":{"message":"The server had an error while processing your request.","type":"server_error","param":null,"code":null}}`, true},
		{`{"error":{"message":"Invalid schema for function 'bash'","type":"invalid_request_error","param":"tools","code":null}}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, head+"data: "+c.event+"\n\n")
		}))
		p := newOpenAI(config.Config{Provider: "openai", Model: "m", APIKey: "k", BaseURL: srv.URL})
		var text strings.Builder
		_, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, func(s string) {
			text.WriteString(s)
		})
		srv.Close()
		if err == nil {
			t.Fatalf("no error from a broken stream: %s", c.event)
		}
		if text.String() != "abc" {
			t.Errorf("streamed %q before the error", text.String())
		}
		if got := Retryable(err); got != c.want {
			t.Errorf("Retryable(%v) %v, want %v", err, got, c.want)
		}
	}
}

// A server that closes the connection in the middle of a reply may end the
// stream with neither an error event nor a read error: what came is not
// the whole reply, and a retry may get it.
func TestAnthropicStreamEndedEarly(t *testing.T) {
	const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"abc"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, stream)
	}))
	defer srv.Close()

	p := newAnthropic(config.Config{Provider: "anthropic", Model: "m", APIKey: "k", BaseURL: srv.URL})
	var text strings.Builder
	resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, func(s string) {
		text.WriteString(s)
	})
	if err == nil {
		t.Fatalf("no error from a stream cut short; reply %+v", resp)
	}
	if text.String() != "abc" {
		t.Errorf("streamed %q before the end", text.String())
	}
	if !Retryable(err) {
		t.Errorf("not retryable: %v", err)
	}
}
