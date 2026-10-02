package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
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
	for _, tc := range []struct {
		provider, effort, err string
	}{
		{"anthropic", "", ""},
		{"anthropic", "max", ""},
		{"anthropic", "minimal", `unknown effort "minimal" for anthropic (want low, medium, high, xhigh, max)`},
		{"openai", "minimal", ""},
		{"openai", "none", ""},
		{"openai", "extreme", `unknown effort "extreme" for openai (want none, minimal, low`},
		{"gemini", "", ""},
		{"gemini", "high", `unknown effort "high" for gemini`},
	} {
		err := CheckEffort(tc.provider, tc.effort)
		if (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s %q: %v, want %q", tc.provider, tc.effort, err, tc.err)
		}
	}
}

func TestNew(t *testing.T) {
	for _, name := range []string{"anthropic", "openai"} {
		p, err := New(config.Config{Provider: name, Model: "m", Effort: "high"})
		if err != nil {
			t.Fatal(err)
		}
		if p.Name() != name || p.Model() != "m" {
			t.Errorf("%s: got %s %s", name, p.Name(), p.Model())
		}
	}
	if _, err := New(config.Config{Provider: "gemini"}); err == nil || err.Error() != `unknown provider "gemini" (want anthropic or openai)` {
		t.Errorf("unknown provider: %v", err)
	}
	if _, err := New(config.Config{Provider: "anthropic", Effort: "minimal"}); err == nil || !strings.Contains(err.Error(), "unknown effort") {
		t.Errorf("bad effort: %v", err)
	}
}
