package main

import (
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
)

// A profile of Anthropic under a top level of OpenAI inherits an effort
// Anthropic has no level for: it is dropped, unless the user gave it.
func TestFitEffort(t *testing.T) {
	cfg := config.Default()
	cfg.Provider, cfg.APIKey = "anthropic", "k"
	prov, err := llm.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		effort  string
		given   bool
		want    string
		dropped bool
	}{
		{"minimal", false, "", true},
		{"minimal", true, "minimal", false},
		{"high", false, "high", false},
		{"high", true, "high", false},
		{"", false, "", false},
	} {
		got, dropped := fitEffort(prov, tc.effort, tc.given)
		if got != tc.want || dropped != tc.dropped {
			t.Errorf("%q, given %v: %q %v", tc.effort, tc.given, got, dropped)
		}
	}
}
