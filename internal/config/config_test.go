package config

import (
	"os"
	"path/filepath"
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
		{"max_tokens = \"1000\"\n", "max_tokens"},
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
