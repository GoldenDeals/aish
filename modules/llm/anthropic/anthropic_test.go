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

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tool"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
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

// sseCall is a stream of text, then a call of name whose input comes in the given parts, ending with tool_use.
func sseCall(text, id, name string, input ...string) string {
	s := sse("tool_use", text)
	s = s[:strings.Index(s, "event: message_delta")]
	ev := func(data string) {
		s += "event: " + data[9:strings.Index(data[9:], `"`)+9] + "\ndata: " + data + "\n\n"
	}
	ev(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":{}}}`)
	for _, part := range input {
		j, _ := json.Marshal(part)
		ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":` + string(j) + `}}`)
	}
	ev(`{"type":"content_block_stop","index":1}`)
	ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`)
	ev(`{"type":"message_stop"}`)
	return s
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
// "system | dialog", after "tools | " if it has any, a * marking a cache breakpoint; answers and errors
// are reported as they are dispatched.
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
			Tools []struct {
				Name        string
				InputSchema json.RawMessage `json:"input_schema"`
			}
			Messages []struct {
				Role    string
				Content []struct {
					Type, Text, Name string
					Input            json.RawMessage
					Content          []struct{ Text string }
					IsError          bool                  `json:"is_error"`
					CacheControl     *struct{ TTL string } `json:"cache_control"`
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "test-model" || body.MaxTokens != 100 || !body.Stream || body.CacheControl != nil {
			t.Errorf("request params: %+v", body)
		}
		if r.Host != "api.invalid" {
			t.Errorf("request to %s, want it through the proxy to the base_url", r.Host)
		}
		if k := r.Header.Get("X-Api-Key"); k != "test" {
			t.Errorf("key %q, want the one from the config", k)
		}
		var tools, sys, msgs []string
		for _, t := range body.Tools {
			tools = append(tools, t.Name+string(t.InputSchema))
		}
		for _, s := range body.System {
			if s.CacheControl != nil && s.CacheControl.TTL == "1h" { // a 5m breakpoint loses its * and fails the test
				s.Text += "*"
			}
			sys = append(sys, s.Text)
		}
		for _, m := range body.Messages {
			var blocks []string
			for _, c := range m.Content {
				text := c.Text
				switch c.Type {
				case "tool_use":
					text = "use:" + c.Name + string(c.Input)
				case "tool_result":
					text = "result:"
					if c.IsError {
						text = "error:"
					}
					for _, r := range c.Content {
						text += r.Text
					}
				}
				if c.CacheControl != nil && c.CacheControl.TTL == "1h" {
					text += "*"
				}
				blocks = append(blocks, text)
			}
			msgs = append(msgs, m.Role+":"+strings.Join(blocks, "+"))
		}
		report := strings.Join(sys, ",") + " | " + strings.Join(msgs, " ")
		if len(tools) > 0 {
			report = strings.Join(tools, ",") + " | " + report
		}
		rq <- report

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
	if err := m.Init(b, cfg{"model": "test-model", "max_tokens": 100, "token": "test", "base_url": "http://api.invalid", "proxy": srv.URL}); err != nil {
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

func TestBadProxy(t *testing.T) {
	if err := (&mod{}).Init(corebus.New(), cfg{"proxy": "localhost:8080"}); err == nil {
		t.Error("a proxy without a scheme is taken")
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

func TestTools(t *testing.T) {
	b, reqs, answers, errs := setup(t,
		reply{200, sseCall("Looking.", "toolu_1", "bash", `{"command":`, `"ls"}`)},
		reply{200, sseCall("", "toolu_2", "nope")},
		reply{200, sse("end_turn", "Done")})
	calls := make(chan tool.Use, 10)
	bus.Sync(b, 0, event.ByName(tool.Call), func(_ context.Context, _ tx.Tx, ev event.Event[tool.Use]) error {
		calls <- ev.Data()
		return nil
	})
	spec := tool.Spec{Name: "bash", Description: "run", Schema: []byte(`{"type":"object","properties":{"command":{"type":"string"}}}`)}
	if err := b.Dispatch(context.Background(), event.New(tool.Declare, spec)); err != nil {
		t.Fatal(err)
	}
	if err := b.Dispatch(context.Background(), event.New(tool.Declare, spec)); err == nil {
		t.Error("a tool is declared twice")
	}

	ctx, stop := llm.WithTurn(context.Background())
	defer stop()
	if err := b.Dispatch(ctx, event.New(llm.UserMessage, "list")); err != nil {
		t.Fatal(err)
	}
	tools := `bash{"type":"object","properties":{"command":{"type":"string"}}} | `
	if got, want := recv(t, reqs), tools+" | user:list*"; got != want {
		t.Errorf("request 1 = %q, want %q", got, want)
	}
	if got, want := recv(t, calls), (tool.Use{ID: "toolu_1", Name: "bash", Input: []byte(`{"command":"ls"}`)}); got.ID != want.ID || got.Name != want.Name || string(got.Input) != string(want.Input) {
		t.Errorf("call = %+v, want %+v", got, want)
	}
	if got := recv(t, answers); !slices.Equal(got, []string{"Looking."}) {
		t.Errorf("answer 1 = %q", got)
	}
	if err := b.Dispatch(context.Background(), event.New(llm.UserMessage, "hey")); err == nil {
		t.Error("a user message got between a call and its result")
	}
	if llm.Turn(ctx).Err() != nil {
		t.Fatal("the turn ended before the answer to the tool result")
	}

	if err := b.Dispatch(context.Background(), event.New(tool.Result, tool.Output{ID: "toolu_1", Text: "a.txt"})); err != nil {
		t.Fatal(err)
	}
	// The model calls a tool nobody declared: the provider answers it itself.
	if got, want := recv(t, reqs), tools+` | user:list assistant:Looking.+use:bash{"command":"ls"} user:result:a.txt*`; got != want {
		t.Errorf("request 2 = %q, want %q", got, want)
	}
	if got, want := recv(t, reqs), tools+` | user:list assistant:Looking.+use:bash{"command":"ls"} user:result:a.txt assistant:use:nope{} user:error:no such tool: nope*`; got != want {
		t.Errorf("request 3 = %q, want %q", got, want)
	}
	recv(t, llm.Turn(ctx).Done())
	recv(t, answers) // empty, before the call of nope
	if got := recv(t, answers); !slices.Equal(got, []string{"Done"}) {
		t.Errorf("answer 3 = %q", got)
	}
	select {
	case e := <-errs:
		t.Errorf("llm.error %q", e)
	case c := <-calls:
		t.Errorf("call of %s, which isn't declared", c.Name)
	default:
	}
}
