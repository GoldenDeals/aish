package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/inebotov/aish/internal/config"
)

func init() {
	Register("openai", "OPENAI_API_KEY", func(cfg config.Config) (Provider, error) {
		return newOpenAI(cfg), nil
	})
}

// openaiAPI is what OpenAI's two APIs share: the client, the levels, the
// models list.
type openaiAPI struct {
	client    openai.Client
	model     string
	effort    string
	maxTokens int64 // max_tokens, 0 to go by the effort
}

func newOpenAIAPI(cfg config.Config) openaiAPI {
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		// Servers that speak OpenAI's API (Ollama, vLLM, LM Studio, proxies
		// such as cliproxyapi) serve it under /v1 and are often given
		// without it.
		base := strings.TrimRight(cfg.BaseURL, "/")
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		opts = append(opts, option.WithBaseURL(base+"/"))
		if strings.HasPrefix(base, "http://") {
			// Such a server is local; the SDK refuses plain HTTP with a key
			// otherwise (and still only allows it for loopback).
			opts = append(opts, option.WithUnsafeAllowHTTP())
		}
	}
	if c := httpClient(cfg); c != nil {
		opts = append(opts, option.WithHTTPClient(c))
	}
	return openaiAPI{client: openai.NewClient(opts...), model: cfg.Model, effort: cfg.Effort, maxTokens: cfg.MaxTokens}
}

func (p *openaiAPI) Model() string { return p.model }

func (p *openaiAPI) Efforts() []string {
	return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
}

func (p *openaiAPI) MaxTokens(effort string) int64 { return replyTokens(p.maxTokens, effort) }

func (p *openaiAPI) Models(ctx context.Context) ([]ModelInfo, error) {
	var out []ModelInfo
	pages := p.client.Models.ListAutoPaging(ctx)
	for pages.Next() {
		out = append(out, ModelInfo{ID: pages.Current().ID})
	}
	return out, pages.Err()
}

// openaiProvider speaks Chat Completions, which servers other than
// OpenAI's speak too. It has no encoding of its own to replay: the model's
// reasoning is lost between turns (openai-responses keeps it).
type openaiProvider struct{ openaiAPI }

func newOpenAI(cfg config.Config) *openaiProvider { return &openaiProvider{newOpenAIAPI(cfg)} }

func (p *openaiProvider) Name() string { return "openai" }

func (p *openaiProvider) Complete(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(p.model),
		Messages: p.messages(req),
		// Without it a stream reports no token counts.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	}
	params.MaxCompletionTokens = openai.Int(p.MaxTokens(p.effort))
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

	var end streamEnd
	stream := p.client.Chat.Completions.NewStreaming(ctx, params, end.option())
	defer stream.Close()
	var acc openai.ChatCompletionAccumulator
	finished := false
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" && onText != nil {
			onText(chunk.Choices[0].Delta.Content)
		}
		for _, c := range chunk.Choices {
			finished = finished || c.FinishReason != ""
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if !finished && !end.done {
		// Closed with nothing wrong to read: what came is not the reply.
		return nil, fmt.Errorf("openai: stream ended early: %w", io.ErrUnexpectedEOF)
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
	if resp.StopReason == "length" {
		resp.StopReason = StopMaxTokens
	}
	if resp.StopReason == "content_filter" {
		resp.StopReason = StopRefusal
	}
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
			out = append(out, openai.ToolMessage(resultText(r), r.CallID))
		}
		if m.Text != "" {
			out = append(out, openai.UserMessage(m.Text))
		}
	}
	return out
}

// resultText is a tool result for OpenAI's APIs, which have no flag for a
// failed call.
func resultText(r ToolResult) string {
	if r.IsError {
		return "ERROR: " + nonEmpty(r.Content)
	}
	return nonEmpty(r.Content)
}
