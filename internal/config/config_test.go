package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T, toml string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", path)
	t.Setenv("AISH_MODEL", "")
	t.Setenv("AISH_EFFORT", "")
	return Load()
}

func TestLoad(t *testing.T) {
	cfg, err := load(t, "model = \"m\"\nmax_tokens = 1000\nfold_lines = -1\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "m" || cfg.MaxTokens != 1000 || cfg.FoldLines != -1 || cfg.MaxSteps != Default().MaxSteps {
		t.Errorf("got %+v", cfg)
	}
}

func TestLoadMissing(t *testing.T) {
	t.Setenv("AISH_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	t.Setenv("AISH_MODEL", "")
	t.Setenv("AISH_EFFORT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != Default().Model {
		t.Errorf("model %q", cfg.Model)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, tc := range []struct{ toml, want string }{
		{"max_token = 1000\n", `unknown key "max_token"`},
		{"modle = \"x\"\nprovider_name = \"y\"\n", `unknown keys "modle", "provider_name"`},
		{"[profiles.work]\nmodel = \"x\"\nmax_tokens = 1\n", `unknown key "profiles.work"`},
		{"x = {y = 1}\na.b = 2\n", `unknown keys "x", "a.b"`},
		{"max_steps = -1\n", "max_steps = -1: must not be negative"},
		{"max_output_bytes = -5\n", "max_output_bytes = -5: must not be negative"},
		{"compact_at = 1.5\n", "compact_at = 1.5: a share of the window"},
		{"max_tokens = \"1000\"\n", "max_tokens"},
		{"mask = [\"ok\", \"(\"]\n", `mask "("`},
		{"[policy]\ndenny = [\"sudo *\"]\n", `unknown key "policy.denny"`},
		{"[policy]\nwrite_outside_home = \"never\"\n", `policy.write_outside_home = "never"`},
	} {
		_, err := load(t, tc.toml)
		if err == nil {
			t.Errorf("%q: no error", tc.toml)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, tc.want) || !strings.Contains(msg, "config.toml: ") {
			t.Errorf("%q: %v, want %q and the path", tc.toml, err, tc.want)
		}
	}
}

func TestLoadPolicy(t *testing.T) {
	cfg, err := load(t, `
[policy]
deny = ["sudo *", "rm -rf /"]
ask  = ["apt install *"]
write_outside_home = "ask"
`)
	if err != nil {
		t.Fatal(err)
	}
	want := Policy{Deny: []string{"sudo *", "rm -rf /"}, Ask: []string{"apt install *"}, WriteOutsideHome: "ask"}
	if !reflect.DeepEqual(cfg.Policy, want) {
		t.Errorf("policy %+v, want %+v", cfg.Policy, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("an empty file:\n%+v\nwant the defaults:\n%+v", cfg, Default())
	}
}

func TestLoadEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model = \"file\"\neffort = \"low\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", path)
	t.Setenv("AISH_MODEL", "env")
	t.Setenv("AISH_EFFORT", "max")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "env" || cfg.Effort != "max" {
		t.Errorf("the environment should win over the file: %q %q", cfg.Model, cfg.Effort)
	}
}

func TestLoadXDG(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("AISH_CONFIG", "")
	t.Setenv("AISH_MODEL", "")
	t.Setenv("AISH_EFFORT", "")
	dir := filepath.Join(root, "config", "aish")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("model = \"xdg\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "xdg" || Dir() != dir || CacheDir() != filepath.Join(root, "cache", "aish") {
		t.Errorf("model %q, dir %s, cache %s", cfg.Model, Dir(), CacheDir())
	}
	if cfg.ToolsDir != filepath.Join(dir, "tools") || cfg.MCPConfig != filepath.Join(dir, "mcp.yaml") ||
		cfg.SessionsDir != filepath.Join(root, "data", "aish", "sessions") {
		t.Errorf("paths: %s %s %s", cfg.ToolsDir, cfg.MCPConfig, cfg.SessionsDir)
	}
}

func TestLoadHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := load(t, `
tools_dir = "~/tools"
policy_dir = "~/policy"
sessions_dir = "~other/sessions"
mcp_config = "/etc/aish/mcp.yaml"
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolsDir != filepath.Join(home, "tools") || cfg.PolicyDir != filepath.Join(home, "policy") {
		t.Errorf("~/ not expanded: %s %s", cfg.ToolsDir, cfg.PolicyDir)
	}
	// Only the user's own home: ~other is someone else's.
	if cfg.SessionsDir != "~other/sessions" || cfg.MCPConfig != "/etc/aish/mcp.yaml" {
		t.Errorf("changed: %s %s", cfg.SessionsDir, cfg.MCPConfig)
	}
}

func TestKey(t *testing.T) {
	t.Setenv("AISH_API_KEY", "")
	t.Setenv("MY_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic")
	t.Setenv("OPENAI_API_KEY", "openai")
	cfg := Default()
	if k := cfg.Key(); k != "anthropic" {
		t.Errorf("anthropic fallback: %q", k)
	}
	cfg.Provider = "openai"
	if k := cfg.Key(); k != "openai" {
		t.Errorf("openai fallback: %q", k)
	}
	t.Setenv("AISH_API_KEY", "aish")
	if k := cfg.Key(); k != "aish" {
		t.Errorf("api_key_env: %q", k)
	}
	cfg.APIKeyEnv = "MY_KEY"
	if k := cfg.Key(); k != "openai" {
		t.Errorf("an empty api_key_env falls back: %q", k)
	}
	cfg.APIKey = "literal"
	if k := cfg.Key(); k != "literal" {
		t.Errorf("api_key: %q", k)
	}
}

func TestReplyTokens(t *testing.T) {
	for _, tc := range []struct {
		max    int64
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
		c := Config{MaxTokens: tc.max, Effort: tc.effort}
		if got := c.ReplyTokens(); got != tc.want {
			t.Errorf("max_tokens %d, effort %q: %d, want %d", tc.max, tc.effort, got, tc.want)
		}
	}
}
