// Package llm is a provider-neutral chat interface with tool calling.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/inebotov/aish/internal/config"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// Message is one turn. A user message carries text and/or tool results; an
// assistant message carries text and/or tool calls.
type Message struct {
	Role        string
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
	// Raw is the provider's encoding of an assistant message, replayed as is
	// when Provider matches (keeps thinking blocks and signatures intact).
	Raw      json.RawMessage
	Provider string
	Model    string
}

type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON schema object with "properties" and "required"
}

type Request struct {
	System   string
	Messages []Message
	Tools    []ToolDef
}

type Response struct {
	Text       string
	ToolCalls  []ToolCall
	Raw        json.RawMessage
	StopReason string
	// Tokens sent, cache included, and received; 0 if the API did not say.
	InputTokens, OutputTokens int
}

// ModelInfo is a model the API offers. Window is 0 when it is not reported.
type ModelInfo struct {
	ID     string
	Window int
	// Efforts are the levels the model takes, when EffortsKnown: proxies
	// such as cliproxyapi do not report them.
	Efforts      []string
	EffortsKnown bool
}

// Efforts are the levels a provider's API takes, from the lightest.
func Efforts(provider string) []string {
	switch provider {
	case "anthropic":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "openai":
		return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	}
	return nil
}

// CheckEffort tells why effort is not a level of provider; "" is the
// model's default and always fits.
func CheckEffort(provider, effort string) error {
	if effort == "" || slices.Contains(Efforts(provider), effort) {
		return nil
	}
	return fmt.Errorf("unknown effort %q for %s (want %s)", effort, provider, strings.Join(Efforts(provider), ", "))
}

type Provider interface {
	Name() string
	Model() string
	// Complete streams the reply, calling onText for each text delta.
	Complete(ctx context.Context, req Request, onText func(string)) (*Response, error)
	Models(ctx context.Context) ([]ModelInfo, error)
}

// replay tells whether an assistant message can be sent in the provider's
// own encoding: thinking signatures are only valid for the model that made
// them.
func replay(m Message, provider, model string) bool {
	return len(m.Raw) > 0 && m.Provider == provider && (m.Model == "" || m.Model == model)
}

func New(cfg config.Config) (Provider, error) {
	if err := CheckEffort(cfg.Provider, cfg.Effort); err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case "anthropic":
		return newAnthropic(cfg), nil
	case "openai":
		return newOpenAI(cfg), nil
	}
	return nil, fmt.Errorf("unknown provider %q (want anthropic or openai)", cfg.Provider)
}

func schemaParts(s map[string]any) (props any, required []string) {
	props = s["properties"]
	if props == nil {
		props = map[string]any{}
	}
	switch r := s["required"].(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			if str, ok := v.(string); ok {
				required = append(required, str)
			}
		}
	}
	return props, required
}
