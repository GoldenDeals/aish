package config

import (
	"strings"
	"testing"
	"time"
)

// tool_timeout is a duration as cache_ttl is, 2 minutes by default, "0"
// for none; what does not parse fails the load, at the top level and in a
// profile alike. A profile takes it from the top level or sets its own.
func TestLoadToolTimeout(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	cfg, err := load(t, "")
	if err != nil || cfg.ToolTimeout != "2m" || cfg.ToolLimit() != 2*time.Minute {
		t.Errorf("default: %q (%v), %v", cfg.ToolTimeout, cfg.ToolLimit(), err)
	}
	for _, tc := range []struct {
		toml string
		want time.Duration
	}{
		{`tool_timeout = "30s"`, 30 * time.Second},
		{`tool_timeout = "1h"`, time.Hour},
		{`tool_timeout = "0"`, 0},
	} {
		cfg, err := load(t, tc.toml+"\n")
		if err != nil || cfg.ToolLimit() != tc.want {
			t.Errorf("%s: %v, %v", tc.toml, cfg.ToolLimit(), err)
		}
	}
	for _, tc := range []struct{ toml, want string }{
		{`tool_timeout = "x"`, `tool_timeout = "x": want days (30d) or hours (12h)`},
		{`tool_timeout = "-1m"`, `tool_timeout = "-1m"`},
		{`tool_timeout = 5`, `tool_timeout`},
		{"[profiles.p]\ntool_timeout = \"x\"", `profiles.p.tool_timeout = "x"`},
	} {
		if _, err := load(t, tc.toml+"\n"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.toml, err, tc.want)
		}
	}
	t.Setenv("AISH_PROFILE", "p")
	cfg, err = load(t, "tool_timeout = \"1m\"\n[profiles.p]\nmodel = \"x\"\n")
	if err != nil || cfg.Profile != "p" || cfg.ToolLimit() != time.Minute {
		t.Errorf("profile p, the top level's: %q, %v, %v", cfg.Profile, cfg.ToolLimit(), err)
	}
	cfg, err = load(t, "tool_timeout = \"1m\"\n[profiles.p]\ntool_timeout = \"10m\"\n")
	if err != nil || cfg.Profile != "p" || cfg.ToolLimit() != 10*time.Minute {
		t.Errorf("profile p, its own: %q, %v, %v", cfg.Profile, cfg.ToolLimit(), err)
	}
	cfg, err = LoadProfile("")
	if err != nil || cfg.ToolLimit() != time.Minute {
		t.Errorf("the top level beside profile p: %v, %v", cfg.ToolLimit(), err)
	}
}
