package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadColdCache(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != "5m" || cfg.CacheMaxAge() != 5*time.Minute || cfg.ColdWarnTokens != 50000 {
		t.Errorf("defaults: cache_ttl %q (%v), cold_warn_tokens %d", cfg.CacheTTL, cfg.CacheMaxAge(), cfg.ColdWarnTokens)
	}
	cfg, err = load(t, "cache_ttl = \"1h\"\ncold_warn_tokens = 0\n")
	if err != nil || cfg.CacheMaxAge() != time.Hour || cfg.ColdWarnTokens != 0 {
		t.Errorf("1h, 0: %v, %d, %v", cfg.CacheMaxAge(), cfg.ColdWarnTokens, err)
	}
	cfg, err = load(t, "cache_ttl = \"0\"\n")
	if err != nil || cfg.CacheMaxAge() != 0 {
		t.Errorf("0: %v, %v", cfg.CacheMaxAge(), err)
	}
	for _, tc := range []struct{ toml, want string }{
		{"cache_ttl = \"abc\"\n", `cache_ttl = "abc": want days (30d) or hours (12h)`},
		{"cache_ttl = \"-5m\"\n", `cache_ttl = "-5m"`},
		{"cold_warn_tokens = -1\n", "cold_warn_tokens = -1: must not be negative"},
	} {
		_, err := load(t, tc.toml)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.toml, err, tc.want)
		}
	}
}

// The keys are config.toml's, as compact_at is: a project refuses them,
// and a profile takes them from the top level, setting none of its own.
func TestColdCacheKeysWhere(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	for _, key := range []string{"compact_at = 0.5", "cache_ttl = \"1h\"", "cold_warn_tokens = 1"} {
		name, _, _ := strings.Cut(key, " ")
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, key+"\n")
		if _, _, err := Project(Default(), root); err == nil || !strings.Contains(err.Error(), `key "`+name+`" is not allowed in a project config`) {
			t.Errorf(".aish.toml with %s: %v", name, err)
		}
		if _, err := load(t, "[profiles.p]\n"+key+"\n"); err == nil || !strings.Contains(err.Error(), `unknown key "profiles.p.`+name+`"`) {
			t.Errorf("[profiles.p] with %s: %v", name, err)
		}
	}
	t.Setenv("AISH_PROFILE", "p")
	cfg, err := load(t, "cache_ttl = \"1h\"\ncold_warn_tokens = 7\n[profiles.p]\nmodel = \"x\"\n")
	if err != nil || cfg.Profile != "p" || cfg.CacheMaxAge() != time.Hour || cfg.ColdWarnTokens != 7 {
		t.Errorf("profile p: %q, %v, %d, %v", cfg.Profile, cfg.CacheMaxAge(), cfg.ColdWarnTokens, err)
	}
}
