// Package anthropic is the Claude provider. It keeps the session context: llm.system fragments make
// the system prompt, user messages and answers make the dialog. Each user.message is answered with
// an llm.answer stream, then llm.error if something failed.
//
// It runs only when the config has its section, so it builds together with other providers.
//
//	anthropic:
//	  model: claude-opus-5-5
//	  max_tokens: 64000
//	  token: sk-ant-...                   # else ANTHROPIC_API_KEY or the `ant auth login` profile
//	  base_url: https://api.anthropic.com # else ANTHROPIC_BASE_URL; a proxy, for one
package anthropic

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

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
const request = "anthropic.request"

// cache lives 1h: the user often thinks longer than 5m between turns. Proxies that pass for Claude Code
// (CLIProxyAPI) add their own 1h breakpoints, and the API wants longer TTLs before shorter ones.
// Breakpoints are explicit, never the top-level one: such proxies cap blocks at 4 without counting it.
var cache = sdk.CacheControlEphemeralParam{TTL: sdk.CacheControlEphemeralTTLTTL1h}

type mod struct {
	b         bus.Bus
	client    sdk.Client
	model     string
	maxTokens int64
	ctx       context.Context // ends on Stop, cancelling requests in flight
	cancel    context.CancelFunc

	// Only sync handlers touch these, so no lock. Both only grow, which keeps the cached prefix valid.
	system []sdk.TextBlockParam
	dialog []sdk.MessageParam
}

func init() { module.Register("anthropic", &mod{}, module.OptIn()) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	c := cfg.Map()
	var opts []option.RequestOption // after the env defaults, so they win
	if v, _ := c["token"].(string); v != "" {
		opts = append(opts, option.WithAPIKey(v))
	}
	if v, _ := c["base_url"].(string); v != "" {
		opts = append(opts, option.WithBaseURL(v))
	}
	m.b, m.client = b, sdk.NewClient(opts...)
	m.model, m.maxTokens = "claude-opus-5-5", 64000
	if v, ok := c["model"].(string); ok {
		m.model = v
	}
	if v, ok := c["max_tokens"].(int); ok {
		m.maxTokens = int64(v)
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
	text := ev.Data()
	// shortcut: an updated fragment is added next to its old version; key fragments by source if that bloats the prompt
	if slices.ContainsFunc(m.system, func(b sdk.TextBlockParam) bool { return b.Text == text }) {
		return nil // injectors may repeat themselves on every message: the prompt and its cache stay as they are
	}
	m.system = append(m.system, sdk.TextBlockParam{Text: text})
	t.OnAbort(func() { m.system = m.system[:len(m.system)-1] })
	return nil
}

func (m *mod) addUser(ctx context.Context, t tx.Tx, ev event.Event[string]) error {
	m.dialog = append(m.dialog, sdk.NewUserMessage(sdk.NewTextBlock(ev.Data())))
	t.OnAbort(func() { m.dialog = m.dialog[:len(m.dialog)-1] })

	// The whole dialog, read back on the next turn. A new block: the one in m.dialog must stay unmarked.
	last := sdk.NewUserMessage(sdk.ContentBlockParamUnion{OfText: &sdk.TextBlockParam{Text: ev.Data(), CacheControl: cache}})
	p := sdk.MessageNewParams{
		Model:     m.model,
		MaxTokens: m.maxTokens,
		System:    slices.Clone(m.system),
		Messages:  append(slices.Clone(m.dialog[:len(m.dialog)-1]), last),
	}
	if n := len(p.System); n > 0 {
		// Keeps the system prompt cached when a new fragment invalidates the dialog after it.
		p.System[n-1].CacheControl = cache
	}
	return t.Emit(ctx, event.New(request, p))
}

func (m *mod) addAnswer(ctx context.Context, t tx.Tx, ev event.Event[[]string]) error {
	if s, ok := ev.(event.Stream[string]); ok {
		if err := s.Wait(ctx); err != nil {
			return err
		}
	}
	text := strings.Join(ev.Data(), "")
	if text == "" {
		return nil // the API rejects empty assistant turns
	}
	m.dialog = append(m.dialog, sdk.NewAssistantMessage(sdk.NewTextBlock(text)))
	t.OnAbort(func() { m.dialog = m.dialog[:len(m.dialog)-1] })
	return nil
}

// shortcut: one request at a time; a user.message sent before the previous answer has started gets answers
// in any order, interleaved in the dialog. Queue requests once a UI can send ahead.
func (m *mod) ask(actx context.Context, ev event.Event[sdk.MessageNewParams]) {
	defer llm.End(actx) // last: after the Error
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	defer context.AfterFunc(llm.Turn(actx), cancel)() // the user stopped the turn
	s := m.client.Messages.NewStreaming(ctx, ev.Data())
	defer s.Close()
	// API errors (key, model, rate limit after the SDK's retries) come before the first event: no empty answer for them.
	if !s.Next() {
		m.fail(actx, cmp.Or(s.Err(), errors.New("empty response stream")))
		return
	}

	ch, done := make(chan string), make(chan error, 1)
	go func() { // the writer must run apart from Dispatch, which waits for the stream to end
		defer close(ch)
		var end sdk.MessageDeltaEventDelta
		for ok := true; ok; ok = s.Next() {
			switch e := s.Current().AsAny().(type) {
			case sdk.ContentBlockDeltaEvent:
				if d, ok := e.Delta.AsAny().(sdk.TextDelta); ok {
					ch <- d.Text
				}
			case sdk.MessageDeltaEvent:
				end = e.Delta
			}
		}
		switch {
		case s.Err() != nil:
			done <- s.Err()
		case end.StopReason == sdk.StopReasonRefusal:
			done <- fmt.Errorf("refused: %s", end.StopDetails.Category)
		case end.StopReason != sdk.StopReasonEndTurn:
			done <- fmt.Errorf("answer incomplete: stop reason %q", end.StopReason)
		default:
			done <- nil
		}
	}()
	// m.ctx, not ctx: a stopped turn commits what it has said, the user saw it.
	err := m.b.Dispatch(m.ctx, event.NewStream(llm.Answer, ch))
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
	_ = m.b.Dispatch(m.ctx, event.New(llm.Error, err.Error())) // a failed error report has nowhere to go
}
