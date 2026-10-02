package llm

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/inebotov/aish/internal/config"
)

type openaiProvider struct {
	client    openai.Client
	model     string
	effort    string
	maxTokens int64
}

func newOpenAI(cfg config.Config) *openaiProvider {
	opts := []option.RequestOption{option.WithAPIKey(cfg.Key())}
	if cfg.BaseURL != "" {
		base := strings.TrimRight(cfg.BaseURL, "/")
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		opts = append(opts, option.WithBaseURL(base+"/"))
		if strings.HasPrefix(base, "http://") {
			// A local proxy such as cliproxyapi; the SDK refuses plain HTTP
			// with a key otherwise (and still only allows it for loopback).
			opts = append(opts, option.WithUnsafeAllowHTTP())
		}
	}
	return &openaiProvider{
		client: openai.NewClient(opts...), model: cfg.Model,
		effort: cfg.Effort, maxTokens: cfg.ReplyTokens(),
	}
}

func (p *openaiProvider) Name() string  { return "openai" }
func (p *openaiProvider) Model() string { return p.model }

func (p *openaiProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	var out []ModelInfo
	pages := p.client.Models.ListAutoPaging(ctx)
	for pages.Next() {
		out = append(out, ModelInfo{ID: pages.Current().ID})
	}
	return out, pages.Err()
}

func (p *openaiProvider) Complete(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(p.model),
		Messages: p.messages(req),
		// Without it a stream reports no token counts.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	}
	params.MaxCompletionTokens = openai.Int(p.maxTokens)
	if p.effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(p.effort)
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(t.Schema),
		}))
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()
	var acc openai.ChatCompletionAccumulator
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" && onText != nil {
			onText(chunk.Choices[0].Delta.Content)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	resp := &Response{
		InputTokens:  int(acc.Usage.PromptTokens),
		CachedTokens: int(acc.Usage.PromptTokensDetails.CachedTokens),
		OutputTokens: int(acc.Usage.CompletionTokens),
	}
	if len(acc.Choices) == 0 {
		return resp, nil
	}
	ch := acc.Choices[0]
	resp.Text = ch.Message.Content
	resp.StopReason = ch.FinishReason
	for _, c := range ch.Message.ToolCalls {
		args := json.RawMessage(c.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Args: args})
	}
	return resp, nil
}

func (p *openaiProvider) messages(req Request) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	if req.System != "" {
		out = append(out, openai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		if m.Role == RoleAssistant {
			a := openai.ChatCompletionAssistantMessageParam{}
			if m.Text != "" {
				a.Content.OfString = openai.String(m.Text)
			}
			for _, c := range m.ToolCalls {
				args := string(c.Args)
				if args == "" {
					args = "{}"
				}
				a.ToolCalls = append(a.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID:       c.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: c.Name, Arguments: args},
					},
				})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &a})
			continue
		}
		for _, r := range m.ToolResults {
			content := nonEmpty(r.Content)
			if r.IsError {
				content = "ERROR: " + content
			}
			out = append(out, openai.ToolMessage(content, r.CallID))
		}
		if m.Text != "" {
			out = append(out, openai.UserMessage(m.Text))
		}
	}
	return out
}
