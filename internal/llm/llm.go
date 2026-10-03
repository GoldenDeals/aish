// Package llm is a provider-neutral chat interface with tool calling. Each
// provider is a file here that registers itself by name (Register): the rest
// of aish knows a provider by the config's name and what its Provider tells,
// never by a name of its own.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
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
	// CachedTokens is the part of InputTokens read from the provider's cache.
	CachedTokens int
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

type Provider interface {
	// Name is the one the provider is registered with.
	Name() string
	Model() string
	// Efforts are the levels of effort the API takes, from the lightest;
	// nil if it takes none.
	Efforts() []string
	// MaxTokens bounds a reply at effort: max_tokens when the config sets
	// it, otherwise what the provider deems enough for the effort.
	MaxTokens(effort string) int64
	// Complete streams the reply, calling onText for each text delta.
	Complete(ctx context.Context, req Request, onText func(string)) (*Response, error)
	Models(ctx context.Context) ([]ModelInfo, error)
}

// defaultProvider is the one a config that names none gets: config's, by
// which a profile tells whether it keeps the top level's provider.
const defaultProvider = config.DefaultProvider

type kind struct {
	keyEnv      string
	newProvider func(config.Config) (Provider, error)
}

var kinds = map[string]kind{}

// Register makes a provider known by name, the config's provider; each
// registers itself from init in its own file. keyEnv is the variable its
// key is read from when the config gives none: the key is wanted before
// there is a provider to ask, since one is made with it.
func Register(name, keyEnv string, newProvider func(config.Config) (Provider, error)) {
	if _, ok := kinds[name]; ok {
		panic("llm: provider " + name + " registered twice")
	}
	kinds[name] = kind{keyEnv, newProvider}
}

func providerName(cfg config.Config) string {
	if cfg.Provider == "" {
		return defaultProvider
	}
	return cfg.Provider
}

// Key is the API key for cfg, its variables read through getenv: api_key,
// the variable api_key_env names, or else the provider's own one.
func Key(cfg config.Config, getenv func(string) string) string {
	return cfg.KeyFrom(getenv, kinds[providerName(cfg)].keyEnv)
}

// New makes the provider cfg names. A key cfg does not carry is read from
// the environment.
func New(cfg config.Config) (Provider, error) {
	cfg.Provider = providerName(cfg)
	k, ok := kinds[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (want %s)", cfg.Provider, strings.Join(slices.Sorted(maps.Keys(kinds)), ", "))
	}
	cfg.APIKey = Key(cfg, os.Getenv)
	p, err := k.newProvider(cfg)
	if err != nil {
		return nil, err
	}
	if err := CheckEffort(p, cfg.Effort); err != nil {
		return nil, err
	}
	return p, nil
}

// CheckEffort tells why effort is not a level of p; "" is the model's
// default and always fits. A nil p, a provider aish does not know, takes
// no level.
func CheckEffort(p Provider, effort string) error {
	switch {
	case effort == "":
		return nil
	case p == nil:
		return fmt.Errorf("unknown effort %q: no provider", effort)
	case !slices.Contains(p.Efforts(), effort):
		return fmt.Errorf("unknown effort %q for %s (want %s)", effort, p.Name(), strings.Join(p.Efforts(), ", "))
	}
	return nil
}

// replyTokens is limit, when the config sets it, or else enough for the
// effort: at xhigh and max the model thinks long, and a reply cut short in
// thinking is lost.
func replyTokens(limit int64, effort string) int64 {
	switch {
	case limit > 0:
		return limit
	case effort == "xhigh" || effort == "max":
		return 64000
	}
	return 32000
}

// replay tells whether an assistant message can be sent in the provider's
// own encoding: thinking signatures are only valid for the model that made
// them.
func replay(m Message, provider, model string) bool {
	return len(m.Raw) > 0 && m.Provider == provider && (m.Model == "" || m.Model == model)
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
