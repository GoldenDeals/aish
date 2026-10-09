package config

import (
	"strings"
	"testing"
	"time"
)

// ask_timeout is a duration as cache_ttl is, 5 minutes by default, "0" for
// none; what does not parse fails the load, at the top level and in a
// profile alike, lest the form wait for ever without a word. A profile
// takes it from the top level or sets its own.
func TestLoadAskTimeout(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	cfg, err := load(t, "")
	if err != nil || cfg.AskTimeout != "5m" || cfg.AskMaxWait() != 5*time.Minute {
		t.Errorf("default: %q (%v), %v", cfg.AskTimeout, cfg.AskMaxWait(), err)
	}
	for _, tc := range []struct {
		toml string
		want time.Duration
	}{
		{`ask_timeout = "30s"`, 30 * time.Second},
		{`ask_timeout = "1d"`, 24 * time.Hour},
		{`ask_timeout = "0"`, 0},
	} {
		cfg, err := load(t, tc.toml+"\n")
		if err != nil || cfg.AskMaxWait() != tc.want {
			t.Errorf("%s: %v, %v", tc.toml, cfg.AskMaxWait(), err)
		}
	}
	for _, tc := range []struct{ toml, want string }{
		{`ask_timeout = "abc"`, `ask_timeout = "abc": want days (30d) or hours (12h)`},
		{`ask_timeout = "-1m"`, `ask_timeout = "-1m"`},
		{`ask_timeout = 5`, `ask_timeout`},
		{"[profiles.p]\nask_timeout = \"abc\"", `profiles.p.ask_timeout = "abc": want days (30d) or hours (12h)`},
		{"[profiles.p]\nask_timeout = \"-1m\"", `profiles.p.ask_timeout = "-1m"`},
	} {
		if _, err := load(t, tc.toml+"\n"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.toml, err, tc.want)
		}
	}
	t.Setenv("AISH_PROFILE", "p")
	cfg, err = load(t, "ask_timeout = \"1m\"\n[profiles.p]\nmodel = \"x\"\n")
	if err != nil || cfg.Profile != "p" || cfg.AskMaxWait() != time.Minute {
		t.Errorf("profile p, the top level's: %q, %v, %v", cfg.Profile, cfg.AskMaxWait(), err)
	}
	cfg, err = load(t, "ask_timeout = \"1m\"\n[profiles.p]\nask_timeout = \"10m\"\n[profiles.q]\nask_timeout = \"0\"\n")
	if err != nil || cfg.Profile != "p" || cfg.AskMaxWait() != 10*time.Minute {
		t.Errorf("profile p, its own: %q, %v, %v", cfg.Profile, cfg.AskMaxWait(), err)
	}
	cfg, err = LoadProfile("q")
	if err != nil || cfg.Profile != "q" || cfg.AskMaxWait() != 0 {
		t.Errorf("profile q, none: %q, %v, %v", cfg.Profile, cfg.AskMaxWait(), err)
	}
	cfg, err = LoadProfile("")
	if err != nil || cfg.AskMaxWait() != time.Minute {
		t.Errorf("the top level beside profile p: %v, %v", cfg.AskMaxWait(), err)
	}
}
