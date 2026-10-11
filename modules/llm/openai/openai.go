// Package openai is the OpenAI provider on the Responses API. It keeps the session context: llm.system fragments
// make the system prompt, user messages and answers make the dialog. Each user.message is answered with
// an llm.answer stream, then llm.error if something failed.
//
// It runs only when the config has its section, so it builds together with other providers.
//
//	openai:
//	  model: gpt-5.5
//	  token: sk-...                         # else OPENAI_API_KEY
//	  base_url: https://api.openai.com/v1   # else OPENAI_BASE_URL; with /v1, as in the OpenAI SDKs
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
	"os"
	"slices"
	"strings"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/contracts/llm"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

// prio puts the context handlers after everyone: injectors hold user.message above it,
// and live readers of llm.answer get it before addAnswer waits for its end.
const prio = math.MinInt

// request carries the context snapshot from the user.message tx to the API call after commit.
const request = "openai.request"

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

type params struct {
	Model    string    `json:"model"`
	Input    []message `json:"input"`
	Stream   bool      `json:"stream"`
	Store    bool      `json:"store"` // false: the API keeps nothing, the whole dialog goes every time
	CacheKey string    `json:"prompt_cache_key"`
}

type mod struct {
	b        bus.Bus
	url      string // of the responses endpoint
	token    string
	model    string
	cacheKey string          // the session's requests share a prefix, and so its cache
	ctx      context.Context // ends on Stop, cancelling requests in flight
	cancel   context.CancelFunc

	// Only sync handlers touch these, so no lock. Both only grow, which keeps the cached prefix valid.
	system []string
	dialog []message
}

func init() { module.Register("openai", &mod{}, module.OptIn()) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	c := cfg.Map()
	token, _ := c["token"].(string)
	m.token = cmp.Or(token, os.Getenv("OPENAI_API_KEY"))
	if m.token == "" {
		return errors.New("no token: set openai.token or OPENAI_API_KEY")
	}
	m.b, m.cacheKey, m.model = b, rand.Text(), "gpt-5.5"
	url, _ := c["base_url"].(string)
	m.url = strings.TrimSuffix(cmp.Or(url, os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1"), "/") + "/responses"
	if v, ok := c["model"].(string); ok {
		m.model = v
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	bus.Sync(b, prio, event.ByName(llm.System), m.addSystem)
	bus.Sync(b, prio, event.ByName(llm.UserMessage), m.addUser)
	bus.Sync(b, prio, event.ByName(llm.Answer), m.addAnswer)
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

func (m *mod) addUser(ctx context.Context, t tx.Tx, ev event.Event[string]) error {
	m.dialog = append(m.dialog, msg("user", ev.Data()))
	t.OnAbort(func() { m.dialog = m.dialog[:len(m.dialog)-1] })

	input := make([]message, 0, len(m.system)+len(m.dialog))
	for _, s := range m.system {
		input = append(input, msg("developer", s))
	}
	p := params{Model: m.model, Input: append(input, m.dialog...), Stream: true, CacheKey: m.cacheKey}
	return t.Emit(ctx, event.New(request, p))
}

func (m *mod) addAnswer(ctx context.Context, t tx.Tx, ev event.Event[[]string]) error {
	if s, ok := ev.(event.Stream[string]); ok {
		if err := s.Wait(ctx); err != nil {
			return err
		}
	}
	text := strings.Join(ev.Data(), "")
	slog.Info("llm answer", "module", "openai", "len", len(text))
	slog.Debug("llm answer", "module", "openai", "text", text)
	if text == "" {
		return nil // nothing for the model to read back
	}
	m.dialog = append(m.dialog, msg("assistant", text))
	t.OnAbort(func() { m.dialog = m.dialog[:len(m.dialog)-1] })
	return nil
}

// shortcut: one request at a time; a user.message sent before the previous answer has started gets answers
// in any order, interleaved in the dialog. Queue requests once a UI can send ahead.
func (m *mod) ask(actx context.Context, ev event.Event[params]) {
	defer llm.End(actx) // last: after the Error
	p := ev.Data()
	slog.Info("llm request", "module", "openai", "model", p.Model, "messages", len(p.Input))
	slog.Debug("llm request", "module", "openai", "params", p)
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	defer context.AfterFunc(llm.Turn(actx), cancel)() // the user stopped the turn
	// Token, model and limit errors come before the answer: no empty answer for them.
	r, err := m.send(ctx, p)
	if err != nil {
		m.fail(actx, err)
		return
	}
	defer r.Body.Close()

	ch, done := make(chan string), make(chan error, 1)
	go func() { // the writer must run apart from Dispatch, which waits for the stream to end
		defer close(ch)
		done <- read(r.Body, ch)
	}()
	// m.ctx, not ctx: a stopped turn commits what it has said, the user saw it.
	err = m.b.Dispatch(m.ctx, event.NewStream(llm.Answer, ch))
	if err != nil {
		cancel() // the answer is rolled back: stop generating it
	}
	if err = errors.Join(err, <-done); err != nil {
		m.fail(actx, err)
	}
}

// fail reports err, or that the user stopped the turn: then err is only the cancellation it caused.
func (m *mod) fail(actx context.Context, err error) {
	if llm.Turn(actx).Err() != nil {
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
	r, err := http.DefaultClient.Do(req)
	if err != nil || r.StatusCode == http.StatusOK {
		return r, err
	}
	defer r.Body.Close()
	detail, _ := io.ReadAll(r.Body)
	return nil, fmt.Errorf("%s: %s: %s", m.url, r.Status, bytes.TrimSpace(detail)) // the url: a 404 is most often a base_url without /v1
}

// read sends the text deltas of the answer to ch and reports how the answer ended.
func read(body io.Reader, ch chan<- string) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(nil, 1<<24) // response.completed repeats the whole answer in one line
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var e struct {
			Type     string
			Delta    string
			Message  string
			Error    struct{ Message string }
			Response struct {
				Error             struct{ Message string }
				IncompleteDetails struct{ Reason string } `json:"incomplete_details"`
			}
		}
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return fmt.Errorf("answer stream: %w", err)
		}
		switch e.Type {
		case "response.output_text.delta":
			ch <- e.Delta
		case "response.completed":
			return nil
		case "response.incomplete":
			return fmt.Errorf("answer incomplete: %s", e.Response.IncompleteDetails.Reason)
		case "response.failed":
			return errors.New(cmp.Or(e.Response.Error.Message, "response failed"))
		case "error":
			return errors.New(cmp.Or(e.Message, e.Error.Message, "stream error"))
		}
	}
	return cmp.Or(sc.Err(), errors.New("answer incomplete: the stream ended early"))
}
