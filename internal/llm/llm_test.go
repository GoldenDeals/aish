package llm

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// sameJSON compares v, as it goes over the wire, with the JSON want.
func sameJSON(t *testing.T, v any, want string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
}

func TestReplay(t *testing.T) {
	raw := json.RawMessage(`{"role":"assistant","content":[]}`)
	for _, tc := range []struct {
		m    Message
		want bool
	}{
		{Message{Raw: raw, Provider: "anthropic", Model: "m"}, true},
		// Entries written before the model was recorded.
		{Message{Raw: raw, Provider: "anthropic"}, true},
		{Message{Raw: raw, Provider: "anthropic", Model: "other"}, false},
		{Message{Raw: raw, Provider: "openai", Model: "m"}, false},
		{Message{Provider: "anthropic", Model: "m"}, false},
	} {
		if got := replay(tc.m, "anthropic", "m"); got != tc.want {
			t.Errorf("%+v: %v", tc.m, got)
		}
	}
}

func TestSchemaParts(t *testing.T) {
	props := map[string]any{"a": map[string]any{"type": "string"}}
	for _, tc := range []struct {
		name     string
		schema   map[string]any
		props    any
		required []string
	}{
		// Built-in tools give []string.
		{"strings", map[string]any{"properties": props, "required": []string{"a"}}, props, []string{"a"}},
		// Schemas decoded from JSON (MCP, skills) give []any.
		{"decoded", map[string]any{"properties": props, "required": []any{"a", 1, "b"}}, props, []string{"a", "b"}},
		{"no required", map[string]any{"properties": props}, props, nil},
		{"empty", map[string]any{}, map[string]any{}, nil},
	} {
		p, r := schemaParts(tc.schema)
		if !reflect.DeepEqual(p, tc.props) || !reflect.DeepEqual(r, tc.required) {
			t.Errorf("%s: %v %v", tc.name, p, r)
		}
	}
}

func TestCheckEffort(t *testing.T) {
	anthropic := newAnthropic(config.Config{})
	openai := newOpenAI(config.Config{})
	for _, tc := range []struct {
		p           Provider
		effort, err string
	}{
		{anthropic, "", ""},
		{anthropic, "max", ""},
		{anthropic, "minimal", `unknown effort "minimal" for anthropic (want low, medium, high, xhigh, max)`},
		{openai, "minimal", ""},
		{openai, "none", ""},
		{openai, "extreme", `unknown effort "extreme" for openai (want none, minimal, low`},
		// A provider the config names but aish does not know.
		{nil, "", ""},
		{nil, "high", `unknown effort "high": no provider`},
	} {
		err := CheckEffort(tc.p, tc.effort)
		if (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%v %q: %v, want %q", tc.p, tc.effort, err, tc.err)
		}
	}
}

// Every provider registered is made by its name and tells that name, which
// replay compares with the one in the journal.
func TestNew(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(kinds)) {
		p, err := New(config.Config{Provider: name, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if p.Name() != name || p.Model() != "m" {
			t.Errorf("%s: got %s %s", name, p.Name(), p.Model())
		}
	}
	if _, err := New(config.Config{Provider: "openai-responses", Effort: "minimal"}); err != nil {
		t.Errorf("a level of the provider: %v", err)
	}
	if p, err := New(config.Config{}); err != nil || p.Name() != defaultProvider {
		t.Errorf("no provider: %v %v", p, err)
	}
	if _, err := New(config.Config{Provider: "gemini"}); err == nil || err.Error() != `unknown provider "gemini" (want anthropic, openai, openai-responses)` {
		t.Errorf("unknown provider: %v", err)
	}
	if _, err := New(config.Config{Provider: "anthropic", Effort: "minimal"}); err == nil || !strings.Contains(err.Error(), "unknown effort") {
		t.Errorf("bad effort: %v", err)
	}
}

func TestKey(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o", "AISH_API_KEY": ""}
	getenv := func(k string) string { return env[k] }
	for _, tc := range []struct{ provider, want string }{
		{"", "a"},
		{"anthropic", "a"},
		{"openai", "o"},
		{"openai-responses", "o"},
		{"gemini", ""},
	} {
		cfg := config.Default()
		cfg.Provider = tc.provider
		if k := Key(cfg, getenv); k != tc.want {
			t.Errorf("%q: %q, want %q", tc.provider, k, tc.want)
		}
	}
	env["AISH_API_KEY"] = "aish"
	if k := Key(config.Default(), getenv); k != "aish" {
		t.Errorf("api_key_env: %q", k)
	}
}

func TestReplyTokens(t *testing.T) {
	for _, tc := range []struct {
		limit  int64
		effort string
		want   int64
	}{
		{0, "", 32000},
		{0, "high", 32000},
		{0, "xhigh", 64000},
		{0, "max", 64000},
		{1000, "max", 1000},
		{100000, "", 100000},
	} {
		if got := replyTokens(tc.limit, tc.effort); got != tc.want {
			t.Errorf("max_tokens %d, effort %q: %d, want %d", tc.limit, tc.effort, got, tc.want)
		}
	}
	// The provider applies it to its config's max_tokens.
	p := newAnthropic(config.Config{MaxTokens: 1000})
	if got := p.MaxTokens("max"); got != 1000 {
		t.Errorf("max_tokens 1000: %d", got)
	}
}
