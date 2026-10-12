// Package openai is the OpenAI provider on the Responses API. It keeps the session context: llm.system fragments
// make the system prompt, tool.declare events the tools, user messages, answers and tool results the dialog.
// Each user.message is answered with an llm.answer stream, then llm.error if something failed;
// an answer that calls tools sends tool.call events and is followed by the answer to their results.
//
// It runs only when the config has its section, so it builds together with other providers.
//
//	openai:
//	  model: gpt-5.5
//	  token: sk-...                         # else OPENAI_API_KEY
//	  base_url: https://api.openai.com/v1   # else OPENAI_BASE_URL; with /v1, as in the OpenAI SDKs
//	  proxy: socks5://127.0.0.1:1080        # http, https, socks5 or socks5h; else HTTPS_PROXY
package openai

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

// prio puts the context handlers after everyone: injectors hold user.message above it,
// and live readers of llm.answer get it before addAnswer waits for its end.
const prio = math.MinInt

// request carries a req from the user.message tx, or the one of the last tool result, to the API call after commit.
const request = "openai.request"

type req struct {
	params params          // the context snapshot
	turn   context.Context // of the user message, never cancelled: llm.Turn and llm.End go by it through tool calls
}

// answer is the llm.answer stream with the tool calls the model makes in it, set before the stream ends:
// the calls go out in the tx of the answer, so no user message gets between them.
type answer struct {
	event.Stream[string]
	uses *[]tool.Use
}

type message struct {
	Type    string    `json:"type"`
	Role    string    `json:"role"`
	Content []content `json:"content"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func msg(role, text string) message {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return message{"message", role, []content{{kind, text}}}
}

type call struct {
	Type      string `json:"type"` // function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type output struct {
	Type   string `json:"type"` // function_call_output
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type function struct {
	Type        string          `json:"type"` // function
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"` // false: strict, the default, wants every property required
}

type params struct {
	Model    string     `json:"model"`
	Input    []any      `json:"input"` // message, call and output
	Tools    []function `json:"tools,omitempty"`
	Stream   bool       `json:"stream"`
	Store    bool       `json:"store"` // false: the API keeps nothing, the whole dialog goes every time
	CacheKey string     `json:"prompt_cache_key"`
}

type mod struct {
	b        bus.Bus
	client   *http.Client
	url      string // of the responses endpoint
	token    string
	model    string
	cacheKey string          // the session's requests share a prefix, and so its cache
	ctx      context.Context // ends on Stop, cancelling requests in flight
	cancel   context.CancelFunc

	// Only sync handlers touch these, so no lock. The first three only grow, which keeps the cached prefix valid.
	system  []string
	tools   []function
	dialog  []any           // message, call and output
	turn    context.Context // of the last user message
	waiting map[string]bool // the tool calls of the last answer without a result
}

func init() { module.Register("openai", &mod{}, module.OptIn()) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	c := cfg.Map()
	token, _ := c["token"].(string)
	m.token = cmp.Or(token, os.Getenv("OPENAI_API_KEY"))
	if m.token == "" {
		return errors.New("no token: set openai.token or OPENAI_API_KEY")
	}
	m.b, m.client, m.cacheKey, m.model = b, http.DefaultClient, rand.Text(), "gpt-5.5"
	if v, _ := c["proxy"].(string); v != "" {
		u, err := url.Parse(v)
		if err != nil || u.Host == "" {
			return fmt.Errorf("proxy %q: want scheme://host:port", v)
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = http.ProxyURL(u)
		m.client = &http.Client{Transport: t}
	}
	base, _ := c["base_url"].(string)
	m.url = strings.TrimSuffix(cmp.Or(base, os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1"), "/") + "/responses"
	if v, ok := c["model"].(string); ok {
		m.model = v
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.waiting = map[string]bool{}
	bus.Sync(b, prio, event.ByName(llm.System), m.addSystem)
	bus.Sync(b, prio, event.ByName(tool.Declare), m.addTool)
	bus.Sync(b, prio, event.ByName(llm.UserMessage), m.addUser)
	bus.Sync(b, prio, event.ByName(llm.Answer), m.addAnswer)
	bus.Sync(b, prio, event.ByName(tool.Result), m.addResult)
	bus.Async(b, event.ByName(request), m.ask)
	return nil
}

func (m *mod) Start(context.Context) error { return nil }

func (m *mod) Stop(context.Context) error {
	m.cancel()
	return nil
}

func (m *mod) addSystem(_ context.Context, t tx.Tx, ev event.Event[string]) error {
	// shortcut: an updated fragment is added next to its old version; key fragments by source if that bloats the prompt
	if slices.Contains(m.system, ev.Data()) {
		return nil // injectors may repeat themselves on every message: the prompt and its cache stay as they are
	}
	m.system = append(m.system, ev.Data())
	t.OnAbort(func() { m.system = m.system[:len(m.system)-1] })
	return nil
}

func (m *mod) addTool(_ context.Context, t tx.Tx, ev event.Event[tool.Spec]) error {
	s := ev.Data()
	if m.declared(s.Name) {
		return fmt.Errorf("tool %q is declared twice", s.Name)
	}
	m.tools = append(m.tools, function{"function", s.Name, s.Description, s.Schema, false})
	t.OnAbort(func() { m.tools = m.tools[:len(m.tools)-1] })
	return nil
}

func (m *mod) declared(name string) bool {
	return slices.ContainsFunc(m.tools, func(f function) bool { return f.Name == name })
}

func (m *mod) addUser(ctx context.Context, t tx.Tx, ev event.Event[string]) error {
	if len(m.waiting) > 0 {
		// shortcut: refused, back to the sender, as tool results must follow their calls; queue it to keep it
		return errors.New("tools are running: send the message once they are done")
	}
	m.dialog = append(m.dialog, msg("user", ev.Data()))
	turn := m.turn
	m.turn = context.WithoutCancel(ctx)
	t.OnAbort(func() { m.dialog, m.turn = m.dialog[:len(m.dialog)-1], turn })
	return m.request(ctx, t)
}

// request asks the model to answer the dialog as it is now.
func (m *mod) request(ctx context.Context, t tx.Tx) error {
	input := make([]any, 0, len(m.system)+len(m.dialog))
	for _, s := range m.system {
		input = append(input, msg("developer", s))
	}
	p := params{Model: m.model, Input: append(input, m.dialog...), Tools: m.tools, Stream: true, CacheKey: m.cacheKey}
	return t.Emit(ctx, event.New(request, req{p, m.turn}))
}

func (m *mod) addAnswer(ctx context.Context, t tx.Tx, ev event.Event[[]string]) error {
	if s, ok := ev.(event.Stream[string]); ok {
		if err := s.Wait(ctx); err != nil {
			return err
		}
	}
	text := strings.Join(ev.Data(), "")
	var uses []tool.Use
	if a, ok := ev.(answer); ok {
		uses = *a.uses
	}
	slog.Info("llm answer", "module", "openai", "len", len(text), "tool_calls", len(uses))
	slog.Debug("llm answer", "module", "openai", "text", text)
	n := len(m.dialog)
	if text != "" { // else nothing for the model to read back
		m.dialog = append(m.dialog, msg("assistant", text))
	}
	for _, u := range uses {
		m.dialog = append(m.dialog, call{"function_call", u.ID, u.Name, string(u.Input)})
		m.waiting[u.ID] = true // all before the first call: a result may come back inside it
	}
	t.OnAbort(func() {
		m.dialog = m.dialog[:n]
		for _, u := range uses {
			delete(m.waiting, u.ID)
		}
	})
	for _, u := range uses {
		var err error
		if m.declared(u.Name) {
			err = t.Emit(ctx, event.New(tool.Call, u))
		} else {
			err = t.Emit(ctx, event.New(tool.Result, tool.Output{ID: u.ID, Text: "no such tool: " + u.Name, Error: true}))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// addResult asks the model again once every call of its answer has a result.
func (m *mod) addResult(ctx context.Context, t tx.Tx, ev event.Event[tool.Output]) error {
	o := ev.Data()
	if !m.waiting[o.ID] {
		return nil // another provider's call, or a second result
	}
	delete(m.waiting, o.ID)
	m.dialog = append(m.dialog, output{"function_call_output", o.ID, o.Text})
	t.OnAbort(func() {
		m.waiting[o.ID] = true
		m.dialog = m.dialog[:len(m.dialog)-1]
	})
	if len(m.waiting) > 0 {
		return nil
	}
	return m.request(ctx, t)
}

// shortcut: one request at a time; a user.message sent before the previous answer has started gets answers
// in any order, interleaved in the dialog. Queue requests once a UI can send ahead.
func (m *mod) ask(_ context.Context, ev event.Event[req]) {
	p, turn := ev.Data().params, ev.Data().turn
	a := answer{uses: new([]tool.Use)}
	defer func() {
		if len(*a.uses) == 0 {
			llm.End(turn) // last: after the Error. An answer with tool calls goes on with the answer to their results
		}
	}()
	slog.Info("llm request", "module", "openai", "model", p.Model, "messages", len(p.Input))
	slog.Debug("llm request", "module", "openai", "params", p)
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	defer context.AfterFunc(llm.Turn(turn), cancel)() // the user stopped the turn
	// Token, model and limit errors come before the answer: no empty answer for them.
	r, err := m.send(ctx, p)
	if err != nil {
		m.fail(turn, err)
		return
	}
	defer r.Body.Close()

	ch, done := make(chan string), make(chan error, 1)
	go func() { // the writer must run apart from Dispatch, which waits for the stream to end
		defer close(ch)
		uses, err := read(r.Body, ch)
		if err == nil {
			*a.uses = uses // before the stream ends, for addAnswer
		}
		done <- err
	}()
	// turn, never cancelled: a stopped turn commits what it has said, the user saw it. Its tool calls get the turn.
	a.Stream = event.NewStream(llm.Answer, ch)
	err = m.b.Dispatch(turn, a)
	if err != nil {
		cancel() // the answer is rolled back: stop generating it
	}
	if err = errors.Join(err, <-done); err != nil {
		*a.uses = nil // rolled back with the answer, or never sent
		m.fail(turn, err)
	}
}

// fail reports err, or that the user stopped the turn: then err is only the cancellation it caused.
func (m *mod) fail(turn context.Context, err error) {
	if llm.Turn(turn).Err() != nil {
		err = errors.New("interrupted")
	}
	slog.Warn("llm error", "module", "openai", "err", err)
	_ = m.b.Dispatch(m.ctx, event.New(llm.Error, err.Error())) // a failed error report has nowhere to go
}

// send starts the answer to p.
func (m *mod) send(ctx context.Context, p params) (*http.Response, error) {
	body, _ := json.Marshal(p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+m.token)
	r, err := m.client.Do(req)
	if err != nil || r.StatusCode == http.StatusOK {
		return r, err
	}
	defer r.Body.Close()
	detail, _ := io.ReadAll(r.Body)
	return nil, fmt.Errorf("%s: %s: %s", m.url, r.Status, bytes.TrimSpace(detail)) // the url: a 404 is most often a base_url without /v1
}

// read sends the text deltas of the answer to ch and reports its tool calls and how it ended.
func read(body io.Reader, ch chan<- string) ([]tool.Use, error) {
	var uses []tool.Use
	sc := bufio.NewScanner(body)
	sc.Buffer(nil, 1<<24) // response.completed repeats the whole answer in one line
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var e struct {
			Type    string
			Delta   string
			Message string
			Error   struct{ Message string }
			Item    struct {
				Type, Name, Arguments string
				CallID                string `json:"call_id"`
			}
			Response struct {
				Error             struct{ Message string }
				IncompleteDetails struct{ Reason string } `json:"incomplete_details"`
			}
		}
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return nil, fmt.Errorf("answer stream: %w", err)
		}
		switch e.Type {
		case "response.output_text.delta":
			ch <- e.Delta
		case "response.output_item.done":
			if e.Item.Type == "function_call" {
				uses = append(uses, tool.Use{ID: e.Item.CallID, Name: e.Item.Name, Input: []byte(cmp.Or(e.Item.Arguments, "{}"))})
			}
		case "response.completed":
			return uses, nil
		case "response.incomplete":
			return nil, fmt.Errorf("answer incomplete: %s", e.Response.IncompleteDetails.Reason)
		case "response.failed":
			return nil, errors.New(cmp.Or(e.Response.Error.Message, "response failed"))
		case "error":
			return nil, errors.New(cmp.Or(e.Message, e.Error.Message, "stream error"))
		}
	}
	return nil, cmp.Or(sc.Err(), errors.New("answer incomplete: the stream ended early"))
}
