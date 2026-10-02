package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"0", 0, true},
		{"0d", 0, true},
		{"30d", 30 * 24 * time.Hour, true},
		{"12h", 12 * time.Hour, true},
		{"720h", 720 * time.Hour, true},
		{"", 0, false},
		{"30", 0, false},
		{"d", 0, false},
		{"-1d", 0, false},
		{"-2h", 0, false},
		{"1.5d", 0, false},
	} {
		got, err := ParseAge(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseAge(%q) = %v, %v", tc.in, got, err)
		}
	}
}

func TestLoadSessionsTTL(t *testing.T) {
	cfg, err := load(t, "")
	if err != nil || cfg.SessionsMaxAge() != 0 {
		t.Fatalf("by default: %v, %v", cfg.SessionsMaxAge(), err)
	}
	cfg, err = load(t, "sessions_ttl = \"30d\"\n")
	if err != nil || cfg.SessionsMaxAge() != 30*24*time.Hour {
		t.Errorf("30d: %v, %v", cfg.SessionsMaxAge(), err)
	}
	_, err = load(t, "sessions_ttl = \"month\"\n")
	if err == nil || !strings.Contains(err.Error(), `sessions_ttl = "month"`) {
		t.Errorf("a bad sessions_ttl: %v", err)
	}
}
