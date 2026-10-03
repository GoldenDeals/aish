package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests of this package take no profile from the shell they run in.
func TestMain(m *testing.M) {
	os.Unsetenv("AISH_PROFILE")
	os.Exit(m.Run())
}

const profiles = `
model = "top"
effort = "high"
max_tokens = 1000
profile = "work"

[profiles.work]
provider = "anthropic"
base_url = "http://127.0.0.1:8317"
model = "claude-opus-5"

[profiles.local]
provider = "openai"
base_url = "http://localhost:11434"
model = "qwen3:32b"
effort = ""
context_window = 32000

[profiles.top]
`

func TestProfileOverTop(t *testing.T) {
	cfg, err := load(t, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "work" || cfg.Provider != "anthropic" || cfg.BaseURL != "http://127.0.0.1:8317" || cfg.Model != "claude-opus-5" {
		t.Errorf("the profile key's: %q %q %q %q", cfg.Profile, cfg.Provider, cfg.BaseURL, cfg.Model)
	}
	// What the profile does not set is the top level's, but for the key:
	// work names its own base_url, so the top level's api_key_env stays out.
	// Its provider is the top level's, so the effort stays in.
	if cfg.Effort != "high" || cfg.MaxTokens != 1000 || cfg.APIKeyEnv != "" {
		t.Errorf("not from the top level: %q %d %q", cfg.Effort, cfg.MaxTokens, cfg.APIKeyEnv)
	}
	if got := cfg.ProfileNames(); !slices.Equal(got, []string{"local", "top", "work"}) {
		t.Errorf("names %q", got)
	}

	// Set to "" in the profile: the model's default, not the top level's.
	local, err := LoadProfile("local")
	if err != nil {
		t.Fatal(err)
	}
	if local.Profile != "local" || local.Provider != "openai" || local.Model != "qwen3:32b" || local.Effort != "" ||
		local.ContextWindow != 32000 || local.MaxTokens != 1000 {
		t.Errorf("local: %+v", local)
	}
	top, err := LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if top.Profile != "" || top.Provider != "" || top.Model != "top" || top.Effort != "high" {
		t.Errorf("the top level alone: %q %q %q %q", top.Profile, top.Provider, top.Model, top.Effort)
	}
	// An empty table is the top level itself.
	empty, err := LoadProfile("top")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Profile != "top" || empty.Model != top.Model || empty.Provider != top.Provider || empty.BaseURL != top.BaseURL {
		t.Errorf("an empty profile: %+v", empty)
	}
}

func TestProfileEnv(t *testing.T) {
	t.Setenv("AISH_PROFILE", "local")
	cfg, err := load(t, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "local" || cfg.Model != "qwen3:32b" {
		t.Errorf("$AISH_PROFILE should win over the profile key: %q %q", cfg.Profile, cfg.Model)
	}
	t.Setenv("AISH_MODEL", "qwen3:8b")
	t.Setenv("AISH_EFFORT", "low")
	if cfg, err = Load(); err != nil || cfg.Model != "qwen3:8b" || cfg.Effort != "low" {
		t.Errorf("$AISH_MODEL and $AISH_EFFORT over the profile: %q %q %v", cfg.Model, cfg.Effort, err)
	}
	// A profile named for the shell is the file's: the variables chose the
	// profile the shell started with.
	if cfg, err = LoadProfile("work"); err != nil || cfg.Profile != "work" || cfg.Model != "claude-opus-5" || cfg.Effort != "high" {
		t.Errorf("LoadProfile: %q %q %q %v", cfg.Profile, cfg.Model, cfg.Effort, err)
	}
}

func TestProfileErrors(t *testing.T) {
	for _, tc := range []struct{ env, toml, want string }{
		{"", "profile = \"home\"\n[profiles.work]\n", `profile: no profile "home" (profiles: work)`},
		{"nope", profiles, `$AISH_PROFILE: no profile "nope" (profiles: local, top, work)`},
		{"nope", "", `$AISH_PROFILE: no profile "nope" (no [profiles.*] tables)`},
		{"", "[profiles.work]\nmodle = \"x\"\n", `unknown key "profiles.work.modle"`},
		{"", "[profiles.work]\nmodel = \"x\"\n[profiles.local.policy]\ndeny = []\n", `unknown key "profiles.local.policy"`},
		{"", "[profiles.work]\nmax_tokens = -1\n", "profiles.work.max_tokens = -1: must not be negative"},
		{"", "[profiles.\"my work\"]\n", `profiles."my work": a profile is named by one word`},
		{"", "[profiles.root]\n", "profiles.root: the name is the top level's"},
	} {
		t.Setenv("AISH_PROFILE", tc.env)
		_, err := load(t, tc.toml)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "config.toml: ") {
			t.Errorf("%q, $AISH_PROFILE %q: %v, want %q and the path", tc.toml, tc.env, err, tc.want)
		}
	}
	t.Setenv("AISH_PROFILE", "")
	if _, err := load(t, profiles); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile("home"); err == nil || !strings.Contains(err.Error(), `no profile "home" (profiles: local, top, work)`) {
		t.Errorf("LoadProfile of an unknown one: %v", err)
	}
}

// A repository chooses no endpoint: profiles stay in config.toml.
func TestProfileNotInProject(t *testing.T) {
	for _, tc := range []struct{ toml, want string }{
		{"profile = \"local\"\n", `key "profile" is not allowed in a project config`},
		{"[profiles.local]\nbase_url = \"http://evil\"\n", `key "profiles" is not allowed in a project config`},
	} {
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, tc.toml)
		if _, _, err := Project(Default(), root); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.toml, err, tc.want)
		}
	}
}
