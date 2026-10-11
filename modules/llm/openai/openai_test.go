package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

const completed = `{"type":"response.completed","response":{"id":"r","status":"completed"}}`

// sse is a Responses API stream of the given text deltas that ends with the end event.
func sse(end string, deltas ...string) string {
	var b strings.Builder
	ev := func(data string) {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(data), &head)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", head.Type, data)
	}
	ev(`{"type":"response.created","response":{"id":"r"}}`)
	for _, d := range deltas {
		text, _ := json.Marshal(d)
		ev(`{"type":"response.output_text.delta","delta":` + string(text) + `}`)
	}
	ev(end)
	return b.String()
}

type reply struct {
	status int
	body   string
}

type fake struct {
	b       bus.Bus
	reqs    chan string // each API request as "role:text ..."
	answers chan []string
	errs    chan string
}

// setup runs the module against a fake API that serves replies in order.
func setup(t *testing.T, replies ...reply) *fake {
	f := &fake{reqs: make(chan string, 10), answers: make(chan []string, 10), errs: make(chan string, 10)}
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body params
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("request to %s with %v", r.URL.Path, r.Header)
		}
		if body.Model != "test-model" || !body.Stream || body.Store || body.CacheKey == "" {
			t.Errorf("request params: %+v", body)
		}
		var msgs []string
		for _, m := range body.Input {
			if m.Type != "message" || (m.Role == "assistant") != (m.Content[0].Type == "output_text") {
				t.Errorf("message: %+v", m)
			}
			msgs = append(msgs, m.Role+":"+m.Content[0].Text)
		}
		f.reqs <- strings.Join(msgs, " ")

		rp := replies[n.Add(1)-1]
		w.WriteHeader(rp.status)
		fmt.Fprint(w, rp.body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL) // no base_url in the config: the env is the fallback
	t.Setenv("OPENAI_API_KEY", "env")    // the config's token wins

	f.b = corebus.New()
	m := &mod{}
	if err := m.Init(f.b, cfg{"model": "test-model", "token": "test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(context.Background()) })
	bus.Sync(f.b, 0, event.ByName(llm.Answer), func(ctx context.Context, _ tx.Tx, ev event.Event[[]string]) error {
		s := ev.(event.Stream[string])
		if err := s.Wait(ctx); err != nil {
			return err
		}
		f.answers <- append([]string{}, s.Data()...) // non-nil: an empty answer is still an answer
		return nil
	})
	bus.Sync(f.b, 0, event.ByName(llm.Error), func(_ context.Context, _ tx.Tx, ev event.Event[string]) error {
		f.errs <- ev.Data()
		return nil
	})
	return f
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		panic("unreachable")
	}
}

// turn sends a user message and waits for its turn to end.
func (f *fake) turn(t *testing.T, text string) {
	t.Helper()
	ctx, stop := llm.WithTurn(context.Background())
	defer stop()
	if err := f.b.Dispatch(ctx, event.New(llm.UserMessage, text)); err != nil {
		t.Fatal(err)
	}
	recv(t, llm.Turn(ctx).Done())
}

func TestDialog(t *testing.T) {
	f := setup(t, reply{200, sse(completed, "Hel", "lo")}, reply{200, sse(completed, "ok")})
	if err := f.b.Dispatch(context.Background(), event.New(llm.System, "mcp")); err != nil {
		t.Fatal(err)
	}
	f.turn(t, "hi")
	if got, want := recv(t, f.reqs), "developer:mcp user:hi"; got != want {
		t.Errorf("request 1 = %q, want %q", got, want)
	}
	if got := recv(t, f.answers); !slices.Equal(got, []string{"Hel", "lo"}) {
		t.Errorf("answer 1 = %q", got)
	}
	f.turn(t, "more")
	if got, want := recv(t, f.reqs), "developer:mcp user:hi assistant:Hello user:more"; got != want {
		t.Errorf("request 2 = %q, want %q", got, want)
	}
	select {
	case e := <-f.errs:
		t.Errorf("llm.error %q", e)
	default:
	}
}

func TestFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  reply
		answer []string // nil when no llm.answer is expected
		err    string   // part of the llm.error
	}{
		{"incomplete", reply{200, sse(`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, "Hel")}, []string{"Hel"}, "max_output_tokens"},
		{"failed", reply{200, sse(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`)}, []string{}, "boom"},
		{"cut", reply{200, sse(`{"type":"response.in_progress"}`, "Hel")}, []string{"Hel"}, "ended early"},
		{"bad token", reply{401, `{"error":{"message":"Incorrect API key"}}`}, nil, "401 Unauthorized: {\"error\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, tc.reply)
			f.turn(t, "hi")
			var e string
			select { // the turn ends after its error
			case e = <-f.errs:
			default:
			}
			if !strings.Contains(e, tc.err) {
				t.Errorf("llm.error = %q, want it to contain %q", e, tc.err)
			}
			var got []string
			select { // an answer is dispatched before the error, so it is in by now
			case got = <-f.answers:
			default:
			}
			if !slices.Equal(got, tc.answer) || (got == nil) != (tc.answer == nil) {
				t.Errorf("answer = %q, want %q", got, tc.answer)
			}
		})
	}
}
