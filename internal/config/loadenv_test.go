package config

import "testing"

// LoadEnv takes the profile, the model and the effort from the environment
// it is given, the shell's, not from the process's.
func TestLoadEnvOfShell(t *testing.T) {
	if _, err := load(t, profiles); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_PROFILE", "top")
	t.Setenv("AISH_MODEL", "process-model")
	t.Setenv("AISH_EFFORT", "max")
	shell := map[string]string{"AISH_PROFILE": "local", "AISH_MODEL": "qwen3:8b", "AISH_EFFORT": "low"}
	cfg, err := LoadEnv(func(k string) string { return shell[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "local" || cfg.Model != "qwen3:8b" || cfg.Effort != "low" {
		t.Errorf("the shell's: %q %q %q", cfg.Profile, cfg.Model, cfg.Effort)
	}

	cfg, err = LoadEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "work" || cfg.Model != "claude-opus-5" || cfg.Effort != "high" {
		t.Errorf("unset in the shell: %q %q %q", cfg.Profile, cfg.Model, cfg.Effort)
	}

	if _, err := LoadEnv(func(k string) string { return map[string]string{"AISH_PROFILE": "gone"}[k] }); err == nil {
		t.Error("a profile config.toml has not")
	}
}
