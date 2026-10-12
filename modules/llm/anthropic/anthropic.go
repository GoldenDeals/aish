// Package anthropic is the Claude provider. It keeps the session context: llm.system fragments make
// the system prompt, tool.declare events the tools, user messages, answers and tool results the dialog.
// Each user.message is answered with an llm.answer stream, then llm.error if something failed;
// an answer that calls tools sends tool.call events and is followed by the answer to their results.
//
// It runs only when the config has its section, so it builds together with other providers.
//
//	anthropic:
//	  model: claude-opus-5-5
//	  max_tokens: 64000
//	  token: sk-ant-...                   # else ANTHROPIC_API_KEY or the `ant auth login` profile
//	  base_url: https://api.anthropic.com # else ANTHROPIC_BASE_URL; a proxy, for one
//	  proxy: socks5://127.0.0.1:1080      # http, https, socks5 or socks5h; else HTTPS_PROXY
package anthropic

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

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
const request = "anthropic.request"

type req struct {
	params sdk.MessageNewParams // the context snapshot
	turn   context.Context      // of the user message, never cancelled: llm.Turn and llm.End go by it through tool calls
}

// answer is the llm.answer stream with the tool calls the model makes in it, set before the stream ends:
// the calls go out in the tx of the answer, so no user message gets between them.
type answer struct {
	event.Stream[string]
	uses *[]tool.Use
}

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

	// Only sync handlers touch these, so no lock. The first three only grow, which keeps the cached prefix valid.
	system  []sdk.TextBlockParam
	tools   []sdk.ToolUnionParam
	dialog  []sdk.MessageParam
	turn    context.Context              // of the last user message
	waiting map[string]bool              // the tool calls of the last answer without a result
	results []sdk.ContentBlockParamUnion // of the last answer's calls so far
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
	if v, _ := c["proxy"].(string); v != "" {
		u, err := url.Parse(v)
		if err != nil || u.Host == "" {
			return fmt.Errorf("proxy %q: want scheme://host:port", v)
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = http.ProxyURL(u)
		opts = append(opts, option.WithHTTPClient(&http.Client{Transport: t}))
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
	text := ev.Data()
	// shortcut: an updated fragment is added next to its old version; key fragments by source if that bloats the prompt
	if slices.ContainsFunc(m.system, func(b sdk.TextBlockParam) bool { return b.Text == text }) {
		return nil // injectors may repeat themselves on every message: the prompt and its cache stay as they are
	}
	m.system = append(m.system, sdk.TextBlockParam{Text: text})
	t.OnAbort(func() { m.system = m.system[:len(m.system)-1] })
	return nil
}

func (m *mod) addTool(_ context.Context, t tx.Tx, ev event.Event[tool.Spec]) error {
	s := ev.Data()
	if m.declared(s.Name) {
		return fmt.Errorf("tool %q is declared twice", s.Name)
	}
	m.tools = append(m.tools, sdk.ToolUnionParam{OfTool: &sdk.ToolParam{
		Name:        s.Name,
		Description: sdk.String(s.Description),
		InputSchema: param.Override[sdk.ToolInputSchemaParam](s.Schema),
	}})
	t.OnAbort(func() { m.tools = m.tools[:len(m.tools)-1] })
	return nil
}

func (m *mod) declared(name string) bool {
	return slices.ContainsFunc(m.tools, func(t sdk.ToolUnionParam) bool { return t.OfTool.Name == name })
}

func (m *mod) addUser(ctx context.Context, t tx.Tx, ev event.Event[string]) error {
	if len(m.waiting) > 0 {
		// shortcut: refused, back to the sender, as tool results must follow their calls; fold it into them to queue it
		return errors.New("tools are running: send the message once they are done")
	}
	m.dialog = append(m.dialog, sdk.NewUserMessage(sdk.NewTextBlock(ev.Data())))
	turn := m.turn
	m.turn = context.WithoutCancel(ctx)
	t.OnAbort(func() { m.dialog, m.turn = m.dialog[:len(m.dialog)-1], turn })
	return m.request(ctx, t)
}

// request asks the model to answer the dialog as it is now.
func (m *mod) request(ctx context.Context, t tx.Tx) error {
	// The whole dialog, read back on the next request. A copy of the last block: the one in m.dialog must stay unmarked.
	msgs := slices.Clone(m.dialog)
	last := &msgs[len(msgs)-1]
	last.Content = slices.Clone(last.Content)
	switch b := &last.Content[len(last.Content)-1]; {
	case b.OfText != nil:
		c := *b.OfText
		c.CacheControl = cache
		b.OfText = &c
	case b.OfToolResult != nil:
		c := *b.OfToolResult
		c.CacheControl = cache
		b.OfToolResult = &c
	}
	p := sdk.MessageNewParams{
		Model:     m.model,
		MaxTokens: m.maxTokens,
		System:    slices.Clone(m.system),
		Tools:     slices.Clone(m.tools),
		Messages:  msgs,
	}
	if n := len(p.System); n > 0 {
		// Keeps the tools and the system prompt cached when a new fragment invalidates the dialog after them.
		p.System[n-1].CacheControl = cache
	}
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
	slog.Info("llm answer", "module", "anthropic", "len", len(text), "tool_calls", len(uses))
	slog.Debug("llm answer", "module", "anthropic", "text", text)
	var blocks []sdk.ContentBlockParamUnion
	if text != "" {
		blocks = append(blocks, sdk.NewTextBlock(text))
	}
	for _, u := range uses {
		blocks = append(blocks, sdk.NewToolUseBlock(u.ID, u.Input, u.Name))
	}
	if len(blocks) == 0 {
		return nil // the API rejects empty assistant turns
	}
	m.dialog = append(m.dialog, sdk.NewAssistantMessage(blocks...))
	t.OnAbort(func() { m.dialog = m.dialog[:len(m.dialog)-1] })

	for _, u := range uses { // all before the first call: a result may come back inside it
		m.waiting[u.ID] = true
		t.OnAbort(func() { delete(m.waiting, u.ID) })
	}
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
	r := sdk.ToolResultBlockParam{ToolUseID: o.ID, IsError: sdk.Bool(o.Error)}
	if o.Text != "" { // the API rejects empty text blocks
		r.Content = []sdk.ToolResultBlockParamContentUnion{{OfText: &sdk.TextBlockParam{Text: o.Text}}}
	}
	m.results = append(m.results, sdk.ContentBlockParamUnion{OfToolResult: &r})
	t.OnAbort(func() {
		m.waiting[o.ID] = true
		m.results = m.results[:len(m.results)-1]
	})
	if len(m.waiting) > 0 {
		return nil
	}
	results := m.results
	m.results = nil
	m.dialog = append(m.dialog, sdk.NewUserMessage(results...))
	t.OnAbort(func() { m.dialog, m.results = m.dialog[:len(m.dialog)-1], results })
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
	slog.Info("llm request", "module", "anthropic", "model", p.Model, "messages", len(p.Messages))
	slog.Debug("llm request", "module", "anthropic", "params", p)
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	defer context.AfterFunc(llm.Turn(turn), cancel)() // the user stopped the turn
	s := m.client.Messages.NewStreaming(ctx, p)
	defer s.Close()
	// API errors (key, model, rate limit after the SDK's retries) come before the first event: no empty answer for them.
	if !s.Next() {
		m.fail(turn, cmp.Or(s.Err(), errors.New("empty response stream")))
		return
	}

	ch, done := make(chan string), make(chan error, 1)
	go func() { // the writer must run apart from Dispatch, which waits for the stream to end
		defer close(ch)
		var msg sdk.Message
		var err error
		for ok := true; ok && err == nil; ok = s.Next() {
			e := s.Current()
			err = msg.Accumulate(e)
			if d, ok := e.AsAny().(sdk.ContentBlockDeltaEvent); ok {
				if t, ok := d.Delta.AsAny().(sdk.TextDelta); ok {
					ch <- t.Text
				}
			}
		}
		switch {
		case err != nil || s.Err() != nil:
			done <- cmp.Or(err, s.Err())
		case msg.StopReason == sdk.StopReasonRefusal:
			done <- fmt.Errorf("refused: %s", msg.StopDetails.Category)
		case msg.StopReason == sdk.StopReasonToolUse:
			var uses []tool.Use
			for _, b := range msg.Content {
				if u, ok := b.AsAny().(sdk.ToolUseBlock); ok {
					uses = append(uses, tool.Use{ID: u.ID, Name: u.Name, Input: u.Input})
				}
			}
			*a.uses = uses // before the stream ends, for addAnswer
			done <- nil
		case msg.StopReason != sdk.StopReasonEndTurn:
			done <- fmt.Errorf("answer incomplete: stop reason %q", msg.StopReason)
		default:
			done <- nil
		}
	}()
	// turn, never cancelled: a stopped turn commits what it has said, the user saw it. Its tool calls get the turn.
	a.Stream = event.NewStream(llm.Answer, ch)
	err := m.b.Dispatch(turn, a)
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
	slog.Warn("llm error", "module", "anthropic", "err", err)
	_ = m.b.Dispatch(m.ctx, event.New(llm.Error, err.Error())) // a failed error report has nowhere to go
}
