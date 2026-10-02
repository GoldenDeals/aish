package llm

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/inebotov/aish/internal/config"
)

type anthropicProvider struct {
	client    anthropic.Client
	model     string
	effort    string
	maxTokens int64
}

func newAnthropic(cfg config.Config) *anthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(cfg.Key())}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &anthropicProvider{
		client: anthropic.NewClient(opts...), model: cfg.Model,
		effort: cfg.Effort, maxTokens: cfg.ReplyTokens(),
	}
}

func (p *anthropicProvider) Name() string  { return "anthropic" }
func (p *anthropicProvider) Model() string { return p.model }

func (p *anthropicProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	var out []ModelInfo
	pages := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	for pages.Next() {
		m := pages.Current()
		info := ModelInfo{ID: m.ID, Window: int(m.MaxInputTokens), EffortsKnown: m.JSON.Capabilities.Valid()}
		if e := m.Capabilities.Effort; e.Supported {
			for _, l := range []struct {
				name string
				ok   bool
			}{
				{"low", e.Low.Supported}, {"medium", e.Medium.Supported}, {"high", e.High.Supported},
				{"xhigh", e.Xhigh.Supported}, {"max", e.Max.Supported},
			} {
				if l.ok {
					info.Efforts = append(info.Efforts, l.name)
				}
			}
		}
		out = append(out, info)
	}
	return out, pages.Err()
}

func (p *anthropicProvider) Complete(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: p.maxTokens,
		Messages:  p.messages(req.Messages),
	}
	if p.effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(p.effort)}
	}
	// Cache breakpoints, in the order the API renders the prompt: tools,
	// system, the conversation so far. The last one moves forward every turn,
	// and the turn after reads everything up to it from the cache.
	for _, t := range req.Tools {
		props, required := schemaParts(t.Schema)
		tp := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: props, Required: required},
		}
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &tp})
	}
	if n := len(params.Tools); n > 0 {
		params.Tools[n-1].OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System, CacheControl: anthropic.NewCacheControlEphemeralParam()}}
	}
	if n := len(params.Messages); n > 0 && params.Messages[n-1].Role == anthropic.MessageParamRoleUser {
		// Only a user message: an assistant one may be a replayed Raw.
		blocks := params.Messages[n-1].Content
		if cc := blocks[len(blocks)-1].GetCacheControl(); cc != nil {
			*cc = anthropic.NewCacheControlEphemeralParam()
		}
	}

	stream := p.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	var msg anthropic.Message
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, err
		}
		if d, ok := ev.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if t, ok := d.Delta.AsAny().(anthropic.TextDelta); ok && onText != nil {
				onText(t.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	u := msg.Usage
	resp := &Response{
		StopReason:   string(msg.StopReason),
		InputTokens:  int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens),
		CachedTokens: int(u.CacheReadInputTokens),
		OutputTokens: int(u.OutputTokens),
	}
	for _, b := range msg.Content {
		switch b := b.AsAny().(type) {
		case anthropic.TextBlock:
			resp.Text += b.Text
		case anthropic.ToolUseBlock:
			args := b.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: args})
		}
	}
	resp.Raw, _ = json.Marshal(msg.ToParam())
	return resp, nil
}

func (p *anthropicProvider) messages(ms []Message) []anthropic.MessageParam {
	var out []anthropic.MessageParam
	for _, m := range ms {
		if m.Role == RoleAssistant {
			if replay(m, p.Name(), p.model) {
				var mp anthropic.MessageParam
				if json.Unmarshal(m.Raw, &mp) == nil {
					out = append(out, mp)
					continue
				}
			}
			var blocks []anthropic.ContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			for _, c := range m.ToolCalls {
				var input any = json.RawMessage(c.Args)
				if len(c.Args) == 0 {
					input = map[string]any{}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(c.ID, input, c.Name))
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropic.NewTextBlock("(no response)"))
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
			continue
		}
		// Tool results must come first in a user turn.
		var blocks []anthropic.ContentBlockParamUnion
		for _, r := range m.ToolResults {
			blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, nonEmpty(r.Content), r.IsError))
		}
		if m.Text != "" {
			blocks = append(blocks, anthropic.NewTextBlock(m.Text))
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, anthropic.NewUserMessage(blocks...))
	}
	return out
}

func nonEmpty(s string) string {
	if s == "" {
		return "(no output)"
	}
	return s
}
