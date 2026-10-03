package config

import "testing"

// A profile naming the top level's provider keeps the top level's effort,
// "" being DefaultProvider: only another provider may have no such level.
func TestProfileEffortSameProvider(t *testing.T) {
	for _, tc := range []struct{ name, toml, profile, effort string }{
		{"default top, same", "effort = \"max\"\n[profiles.p]\nprovider = \"anthropic\"\n", "p", "max"},
		{"default top, another", "effort = \"max\"\n[profiles.p]\nprovider = \"openai\"\n", "p", ""},
		{"named top, same", "provider = \"openai\"\neffort = \"minimal\"\n[profiles.p]\nprovider = \"openai\"\n", "p", "minimal"},
		{"named top, another", "provider = \"openai\"\neffort = \"minimal\"\n[profiles.p]\nprovider = \"anthropic\"\n", "p", ""},
		{"named top, empty", "provider = \"anthropic\"\neffort = \"max\"\n[profiles.p]\nprovider = \"\"\n", "p", "max"},
	} {
		if _, err := load(t, tc.toml); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		cfg, err := LoadProfile(tc.profile)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if cfg.Effort != tc.effort {
			t.Errorf("%s: effort %q, want %q", tc.name, cfg.Effort, tc.effort)
		}
	}
}
