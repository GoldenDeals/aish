package config

import "testing"

// The top level's API key goes only where the top level's endpoint does.
func TestProfileKey(t *testing.T) {
	const toml = `
api_key = "top-key"
api_key_env = "TOP_ENV"

[profiles.model]
model = "other"

[profiles.url]
base_url = "http://localhost:11434"

[profiles.provider]
provider = "openai"

[profiles.local]
base_url = "http://localhost:11434"
api_key_env = "LOCAL_ENV"

[profiles.env]
api_key_env = "P_ENV"
`
	if _, err := load(t, toml); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ profile, key, env string }{
		{"model", "top-key", "TOP_ENV"},
		{"url", "", ""},
		{"provider", "", ""},
		{"local", "", "LOCAL_ENV"},
		{"env", "", "P_ENV"},
		{"", "top-key", "TOP_ENV"},
	} {
		cfg, err := LoadProfile(tc.profile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.APIKey != tc.key || cfg.APIKeyEnv != tc.env {
			t.Errorf("profile %q: api_key %q, api_key_env %q; want %q, %q", tc.profile, cfg.APIKey, cfg.APIKeyEnv, tc.key, tc.env)
		}
	}
}
