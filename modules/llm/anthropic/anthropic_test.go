package anthropic

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

	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/contracts/llm"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

// sse is a Messages API stream of the given text deltas that ends with stop.
func sse(stop string, deltas ...string) string {
	var b strings.Builder
	ev := func(data string) {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(data), &head)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", head.Type, data)
	}
	ev(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	ev(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	for _, d := range deltas {
		text, _ := json.Marshal(d)
		ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(text) + `}}`)
	}
	ev(`{"type":"content_block_stop","index":0}`)
	ev(`{"type":"message_delta","delta":{"stop_reason":"` + stop + `"},"usage":{"output_tokens":1}}`)
	ev(`{"type":"message_stop"}`)
	return b.String()
}

// unfinished is a stream of the given deltas that is still going.
func unfinished(deltas ...string) string {
	s := sse("end_turn", deltas...)
	return s[:strings.Index(s, "event: content_block_stop")]
}

type reply struct {
	status int
	body   string
}

// setup runs the module against a fake API serving replies in order. Each request is reported as
// "system | dialog", a * marking a cache breakpoint; answers and errors are reported as they are dispatched.
func setup(t *testing.T, replies ...reply) (b bus.Bus, reqs <-chan string, answers <-chan []string, errs <-chan string) {
	rq := make(chan string, len(replies))
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model        string
			MaxTokens    int `json:"max_tokens"`
			Stream       bool
			CacheControl *struct{ TTL string } `json:"cache_control"`
			System       []struct {
				Text         string
				CacheControl *struct{ TTL string } `json:"cache_control"`
			}
			Messages []struct {
				Role    string
				Content []struct {
					Text         string
					CacheControl *struct{ TTL string } `json:"cache_control"`
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "test-model" || body.MaxTokens != 100 || !body.Stream || body.CacheControl != nil {
			t.Errorf("request params: %+v", body)
		}
		if k := r.Header.Get("X-Api-Key"); k != "test" {
			t.Errorf("key %q, want the one from the config", k)
		}
		var sys, msgs []string
		for _, s := range body.System {
			if s.CacheControl != nil && s.CacheControl.TTL == "1h" { // a 5m breakpoint loses its * and fails the test
				s.Text += "*"
			}
			sys = append(sys, s.Text)
		}
		for _, m := range body.Messages {
			c := m.Content[0]
			if c.CacheControl != nil && c.CacheControl.TTL == "1h" {
				c.Text += "*"
			}
			msgs = append(msgs, m.Role+":"+c.Text)
		}
		rq <- strings.Join(sys, ",") + " | " + strings.Join(msgs, " ")

		rp := replies[n.Add(1)-1]
		if rp.status == http.StatusOK {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(rp.status)
		fmt.Fprint(w, rp.body)
		if rp.status == http.StatusOK && !strings.Contains(rp.body, "message_stop") {
			w.(http.Flusher).Flush()
			<-r.Context().Done() // an unfinished stream stays open, like a long answer, until the client leaves
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", "http://env.invalid") // the config wins over the env
	t.Setenv("ANTHROPIC_API_KEY", "env")

	b = corebus.New()
	m := &mod{}
	if err := m.Init(b, cfg{"model": "test-model", "max_tokens": 100, "token": "test", "base_url": srv.URL}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(context.Background()) })

	ans, er := make(chan []string, 10), make(chan string, 10)
	bus.Sync(b, 0, event.ByName(llm.Answer), func(ctx context.Context, _ tx.Tx, ev event.Event[[]string]) error {
		s := ev.(event.Stream[string])
		if err := s.Wait(ctx); err != nil {
			return err
		}
		ans <- append([]string{}, s.Data()...) // non-nil: an empty answer is still an answer
		return nil
	})
	bus.Sync(b, 0, event.ByName(llm.Error), func(_ context.Context, _ tx.Tx, ev event.Event[string]) error {
		er <- ev.Data()
		return nil
	})
	return b, rq, ans, er
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

func dispatch(t *testing.T, b bus.Bus, name, text string) {
	t.Helper()
	if err := b.Dispatch(context.Background(), event.New(name, text)); err != nil {
		t.Fatal(err)
	}
}

func TestDialog(t *testing.T) {
	b, reqs, answers, errs := setup(t, reply{200, sse("end_turn", "Hel", "lo")}, reply{200, sse("end_turn", "ok")})
	// An injector adds its fragment ahead of every user message; the repeat must not touch the prompt.
	bus.Sync(b, 10, event.ByName(llm.UserMessage), func(ctx context.Context, xt tx.Tx, _ event.Event[string]) error {
		return xt.Emit(ctx, event.New(llm.System, "skills"))
	})

	dispatch(t, b, llm.System, "mcp") // once, at session start
	dispatch(t, b, llm.UserMessage, "hi")
	if got, want := recv(t, reqs), "mcp,skills* | user:hi*"; got != want {
		t.Errorf("request 1 = %q, want %q", got, want)
	}
	if got := recv(t, answers); !slices.Equal(got, []string{"Hel", "lo"}) {
		t.Errorf("answer 1 = %q", got)
	}

	dispatch(t, b, llm.System, "notes")
	ctx, stop := llm.WithTurn(context.Background())
	defer stop()
	if err := b.Dispatch(ctx, event.New(llm.UserMessage, "again")); err != nil {
		t.Fatal(err)
	}
	if got, want := recv(t, reqs), "mcp,skills,notes* | user:hi assistant:Hello user:again*"; got != want {
		t.Errorf("request 2 = %q, want %q", got, want)
	}
	recv(t, llm.Turn(ctx).Done()) // the answer is over
	recv(t, answers)
	select {
	case e := <-errs:
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
		{"max tokens", reply{200, sse("max_tokens", "Hel")}, []string{"Hel"}, "max_tokens"},
		{"refusal", reply{200, sse("refusal")}, []string{}, "refused"},
		{"api error", reply{401, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`}, nil, "401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _, answers, errs := setup(t, tc.reply)
			ctx, stop := llm.WithTurn(context.Background())
			defer stop()
			if err := b.Dispatch(ctx, event.New(llm.UserMessage, "hi")); err != nil {
				t.Fatal(err)
			}
			recv(t, llm.Turn(ctx).Done())
			var e string
			select { // the turn ends after its error
			case e = <-errs:
			default:
			}
			if !strings.Contains(e, tc.err) {
				t.Errorf("llm.error = %q, want it to contain %q", e, tc.err)
			}
			var got []string
			select { // an answer is dispatched before the error, so it is in by now
			case got = <-answers:
			default:
			}
			if !slices.Equal(got, tc.answer) || (got == nil) != (tc.answer == nil) {
				t.Errorf("answer = %q, want %q", got, tc.answer)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	b, reqs, answers, errs := setup(t, reply{200, unfinished("Hel")}, reply{200, sse("end_turn", "ok")})
	first := make(chan struct{}, 1)
	bus.Sync(b, 1, event.ByName(llm.Answer), func(ctx context.Context, _ tx.Tx, ev event.Event[[]string]) error {
		ctx, stop := context.WithCancel(ctx)
		defer stop()
		<-ev.(event.Stream[string]).Read(ctx)
		select { // the answer is streaming
		case first <- struct{}{}:
		default:
		}
		return nil
	})

	ctx, stop := llm.WithTurn(context.Background())
	if err := b.Dispatch(ctx, event.New(llm.UserMessage, "q")); err != nil {
		t.Fatal(err)
	}
	recv(t, reqs)
	recv(t, first)
	stop()
	if got := recv(t, answers); !slices.Equal(got, []string{"Hel"}) { // what was said stays
		t.Errorf("answer = %q, want the part before stop", got)
	}
	if e := recv(t, errs); e != "interrupted" {
		t.Errorf("llm.error = %q, want interrupted", e)
	}

	// The bus is free and the model sees where it was stopped.
	dispatch(t, b, llm.UserMessage, "again")
	if got, want := recv(t, reqs), " | user:q assistant:Hel user:again*"; got != want {
		t.Errorf("request 2 = %q, want %q", got, want)
	}
	recv(t, answers)
}
