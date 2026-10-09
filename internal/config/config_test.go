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
		{"[profiless.work]\nmodel = \"x\"\nmax_tokens = 1\n", `unknown key "profiless.work"`},
		{"x = {y = 1}\na.b = 2\n", `unknown keys "x", "a.b"`},
		{"max_steps = -1\n", "max_steps = -1: must not be negative"},
		{"max_output_bytes = -5\n", "max_output_bytes = -5: must not be negative"},
		{"compact_at = 1.5\n", "compact_at = 1.5: a share of the window"},
		{"max_tokens = \"1000\"\n", "max_tokens"},
		{"mask = [\"ok\", \"(\"]\n", `mask "("`},
		{"[policy]\ndenny = [\"sudo *\"]\n", `unknown key "policy.denny"`},
		{"[policy]\nwrite_outside_home = \"never\"\n", `policy.write_outside_home = "never"`},
		{"hooks_fail = \"maybe\"\n", `hooks_fail = "maybe"`},
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
	want := Policy{Deny: []string{"sudo *", "rm -rf /"}, Ask: []string{"apt install *"}, WriteOutsideHome: "ask", Builtin: true}
	if !reflect.DeepEqual(cfg.Policy, want) {
		t.Errorf("policy %+v, want %+v", cfg.Policy, want)
	}
}

// hooks_fail is the user's to set: a repository must not loosen a guard
// of theirs.
func TestLoadHooksFail(t *testing.T) {
	cfg, err := load(t, "hooks_fail = \"deny\"\n")
	if err != nil || cfg.HooksFail != "deny" {
		t.Errorf("hooks_fail %q, %v", cfg.HooksFail, err)
	}
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	repo(t, root, "hooks_fail = \"allow\"\n")
	if _, _, err := Project(cfg, root); err == nil || !strings.Contains(err.Error(), `key "hooks_fail" is not allowed in a project config`) {
		t.Errorf("hooks_fail in a project: %v", err)
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
	env := map[string]string{"PROVIDER_KEY": "provider"}
	getenv := func(k string) string { return env[k] }
	cfg := Default()
	if k := cfg.KeyFrom(getenv, "PROVIDER_KEY"); k != "provider" {
		t.Errorf("the provider's variable: %q", k)
	}
	env["AISH_API_KEY"] = "aish"
	if k := cfg.KeyFrom(getenv, "PROVIDER_KEY"); k != "aish" {
		t.Errorf("api_key_env: %q", k)
	}
	cfg.APIKeyEnv = "MY_KEY"
	if k := cfg.KeyFrom(getenv, "PROVIDER_KEY"); k != "provider" {
		t.Errorf("an empty api_key_env falls back: %q", k)
	}
	if k := cfg.KeyFrom(getenv, ""); k != "" {
		t.Errorf("a provider with no variable: %q", k)
	}
	cfg.APIKey = "literal"
	if k := cfg.KeyFrom(getenv, "PROVIDER_KEY"); k != "literal" {
		t.Errorf("api_key: %q", k)
	}
}
