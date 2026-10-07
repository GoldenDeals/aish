package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/GoldenDeals/aish/internal/config"
)

func init() {
	Register("openai-responses", "OPENAI_API_KEY", func(cfg config.Config) (Provider, error) {
		return newResponses(cfg), nil
	})
}

// responsesProvider speaks OpenAI's Responses API. Unlike Chat Completions
// it hands out the model's reasoning, encrypted, for the next turn to go on
// from: Raw keeps the output items, replayed as they came. Nothing is
// stored on OpenAI's side; the journal is the conversation.
type responsesProvider struct{ openaiAPI }

func newResponses(cfg config.Config) *responsesProvider {
	return &responsesProvider{newOpenAIAPI(cfg)}
}

func (p *responsesProvider) Name() string { return "openai-responses" }

func (p *responsesProvider) Complete(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	params := responses.ResponseNewParams{
		Model:           shared.ResponsesModel(p.model),
		Input:           responses.ResponseNewParamsInputUnion{OfInputItemList: p.input(req.Messages)},
		MaxOutputTokens: openai.Int(p.MaxTokens(p.effort)),
		Store:           openai.Bool(false),
		Include:         []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
	}
	if req.System != "" {
		params.Instructions = openai.String(req.System)
	}
	if p.effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(p.effort)}
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  t.Schema,
			// Strict is the default here, and it wants every argument required.
			Strict: openai.Bool(false),
		}})
	}

	stream := p.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()
	var done *responses.Response
	for stream.Next() {
		switch ev := stream.Current().AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			if onText != nil {
				onText(ev.Delta)
			}
		case responses.ResponseCompletedEvent:
			done = &ev.Response
		case responses.ResponseIncompleteEvent:
			done = &ev.Response
		case responses.ResponseFailedEvent:
			e := &APIError{Code: string(ev.Response.Error.Code), Message: ev.Response.Error.Message}
			if *e == (APIError{}) {
				e.Message = "the response failed"
			}
			return nil, e
		case responses.ResponseErrorEvent:
			// Not the SDK's StreamError: this event has no error object.
			return nil, &APIError{Code: ev.Code, Message: ev.Message}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if done == nil {
		// Closed with nothing wrong to read: what came is not the reply.
		return nil, fmt.Errorf("openai-responses: stream ended early: %w", io.ErrUnexpectedEOF)
	}
	return p.response(done), nil
}

func (p *responsesProvider) response(r *responses.Response) *Response {
	resp := &Response{
		Text:         r.OutputText(),
		StopReason:   string(r.Status),
		InputTokens:  int(r.Usage.InputTokens),
		CachedTokens: int(r.Usage.InputTokensDetails.CachedTokens),
		OutputTokens: int(r.Usage.OutputTokens),
	}
	switch r.IncompleteDetails.Reason {
	case "max_output_tokens":
		resp.StopReason = StopMaxTokens // as the agent knows a reply cut short
	case "content_filter":
		resp.StopReason = StopRefusal
	}
	var raw []json.RawMessage
	for _, it := range r.Output {
		switch it.Type {
		case "function_call":
			c := it.AsFunctionCall()
			args := json.RawMessage(c.Arguments)
			if !json.Valid(args) {
				// Cut short; the call still needs a result.
				args = json.RawMessage("{}")
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: c.CallID, Name: c.Name, Args: args})
		case "reasoning":
			// Without its encrypted content the item refers to what was
			// not stored, and the API refuses it.
			if it.AsReasoning().EncryptedContent == "" {
				continue
			}
		}
		raw = append(raw, json.RawMessage(it.RawJSON()))
	}
	if len(raw) > 0 {
		resp.Raw, _ = json.Marshal(raw)
	}
	return resp
}

func (p *responsesProvider) input(ms []Message) responses.ResponseInputParam {
	var out responses.ResponseInputParam
	for _, m := range ms {
		if m.Role == RoleAssistant {
			var items []json.RawMessage
			if replay(m, p.Name(), p.model) && json.Unmarshal(m.Raw, &items) == nil {
				for _, it := range items {
					out = append(out, param.Override[responses.ResponseInputItemUnionParam](it))
				}
				continue
			}
			if m.Text != "" {
				out = append(out, responses.ResponseInputItemParamOfMessage(m.Text, responses.EasyInputMessageRoleAssistant))
			}
			for _, c := range m.ToolCalls {
				args := string(c.Args)
				if args == "" {
					args = "{}"
				}
				out = append(out, responses.ResponseInputItemParamOfFunctionCall(args, c.ID, c.Name))
			}
			continue
		}
		for _, r := range m.ToolResults {
			item := responses.ResponseInputItemParamOfFunctionCallOutput(resultText(r))
			item.OfFunctionCallOutput.CallID = openai.String(r.CallID)
			out = append(out, item)
		}
		if m.Text != "" {
			out = append(out, responses.ResponseInputItemParamOfMessage(m.Text, responses.EasyInputMessageRoleUser))
		}
	}
	return out
}
