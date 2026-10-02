package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/tools"
)

// flakyProvider streams text and fails at its first attempts, then answers
// as the fakeProvider it wraps: replies are indexed by attempt, failed ones
// included.
type flakyProvider struct {
	*fakeProvider
	text  string
	fails []error
	// failed runs after each failure.
	failed func(n int)
}

func (f *flakyProvider) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	n := len(f.requests)
	if n >= len(f.fails) {
		return f.fakeProvider.Complete(ctx, req, onText)
	}
	f.requests = append(f.requests, req)
	onText(f.text)
	if f.failed != nil {
		f.failed(n)
	}
	return nil, f.fails[n]
}

func fastRetries(t *testing.T, wait time.Duration) {
	t.Helper()
	old := retryWaits
	retryWaits = []time.Duration{wait, wait, wait}
	t.Cleanup(func() { retryWaits = old })
}

var errOverloaded = fmt.Errorf("stream: %w", io.ErrUnexpectedEOF)

// A reply broken off by the API is asked for again; the journal gets only
// the one that came whole.
func TestRetryOverloaded(t *testing.T) {
	fastRetries(t, time.Millisecond)
	fake := &fakeProvider{replies: []*llm.Response{nil, {Text: "done"}}}
	a, j, _, ui, cwd := newAgent(t, fake)
	a.Provider = &flakyProvider{fakeProvider: fake, text: "abc", fails: []error{errOverloaded}}
	if err := a.Start(context.Background(), "tell me", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Fatalf("journal %s", got)
	}
	if e := j.es[1]; e.Text != "done" {
		t.Errorf("kept %q", e.Text)
	}
	out := ui.String()
	if !strings.Contains(out, "abc\n") || !strings.Contains(out, "retrying in 1ms (1/3)]") || !strings.Contains(out, "done") {
		t.Errorf("terminal %q", out)
	}
	if strings.Index(out, "abc") > strings.Index(out, "retrying") || strings.Index(out, "retrying") > strings.Index(out, "done") {
		t.Errorf("terminal out of order: %q", out)
	}
}

// When the retries run out, what the last attempt streamed is kept with
// the reason it ended.
func TestRetriesRunOut(t *testing.T) {
	fastRetries(t, time.Millisecond)
	fake := &fakeProvider{}
	a, j, _, ui, cwd := newAgent(t, fake)
	a.Provider = &flakyProvider{fakeProvider: fake, text: "abc", fails: []error{errOverloaded, errOverloaded, errOverloaded, errOverloaded}}
	err := a.Start(context.Background(), "tell me", tools.Exec{Dir: cwd})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err %v", err)
	}
	if len(fake.requests) != 4 {
		t.Errorf("%d attempts, want 4", len(fake.requests))
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Fatalf("journal %s", got)
	}
	if e := j.es[1]; e.Text != "abc\n[cut off by an API error: "+errOverloaded.Error()+"]" {
		t.Errorf("kept %q", e.Text)
	}
	if n := strings.Count(ui.String(), "retrying"); n != 3 {
		t.Errorf("%d retry notes:\n%s", n, ui.String())
	}
}

// A request the API rejects is not sent again.
func TestNoRetryOfRejected(t *testing.T) {
	fastRetries(t, time.Millisecond)
	fake := &fakeProvider{replies: []*llm.Response{nil, {Text: "done"}}}
	a, j, _, ui, cwd := newAgent(t, fake)
	a.Provider = &flakyProvider{fakeProvider: fake, text: "abc", fails: []error{errors.New("400 Bad Request")}}
	if err := a.Start(context.Background(), "tell me", tools.Exec{Dir: cwd}); err == nil {
		t.Fatal("no error")
	}
	if len(fake.requests) != 1 || strings.Contains(ui.String(), "retrying") {
		t.Errorf("%d attempts, terminal %q", len(fake.requests), ui.String())
	}
	if e := j.es[len(j.es)-1]; e.Text != "abc\n[cut off by an API error: 400 Bad Request]" {
		t.Errorf("kept %q", e.Text)
	}
}

// Ctrl+C while waiting to retry ends the request at once.
func TestRetryInterrupted(t *testing.T) {
	fastRetries(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeProvider{}
	a, j, _, ui, cwd := newAgent(t, fake)
	a.Provider = &flakyProvider{fakeProvider: fake, text: "abc", fails: []error{errOverloaded},
		failed: func(int) { time.AfterFunc(20*time.Millisecond, cancel) }}
	start := time.Now()
	err := a.Start(ctx, "tell me", tools.Exec{Dir: cwd})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
	if !strings.Contains(ui.String(), "retrying in 1h0m0s (1/3)") {
		t.Errorf("terminal %q", ui.String())
	}
	// The cut attempt was left for a new one: there is none to keep.
	if got := kinds(j.es); got != "user" {
		t.Errorf("journal %s", got)
	}
}
