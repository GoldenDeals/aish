package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ask_bell is off by default; on at the top level or in a profile, which
// takes it from the top level or sets its own. A project may not set it:
// the terminal is the user's, not the repository's.
func TestLoadAskBell(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	if Default().AskBell {
		t.Error("on by default")
	}
	cfg, err := load(t, "")
	if err != nil || cfg.AskBell {
		t.Errorf("default: %v, %v", cfg.AskBell, err)
	}
	cfg, err = load(t, "ask_bell = true\n")
	if err != nil || !cfg.AskBell {
		t.Errorf("on: %v, %v", cfg.AskBell, err)
	}
	if _, err := load(t, "ask_bell = \"yes\"\n"); err == nil || !strings.Contains(err.Error(), "ask_bell") {
		t.Errorf("a string: %v", err)
	}
	t.Setenv("AISH_PROFILE", "p")
	cfg, err = load(t, "ask_bell = true\n[profiles.p]\nmodel = \"x\"\n")
	if err != nil || cfg.Profile != "p" || !cfg.AskBell {
		t.Errorf("profile p, the top level's: %q, %v, %v", cfg.Profile, cfg.AskBell, err)
	}
	cfg, err = load(t, "ask_bell = true\n[profiles.p]\nask_bell = false\n[profiles.q]\nmodel = \"x\"\n")
	if err != nil || cfg.Profile != "p" || cfg.AskBell {
		t.Errorf("profile p, its own: %q, %v, %v", cfg.Profile, cfg.AskBell, err)
	}
	cfg, err = LoadProfile("q")
	if err != nil || cfg.Profile != "q" || !cfg.AskBell {
		t.Errorf("profile q, the top level's: %q, %v, %v", cfg.Profile, cfg.AskBell, err)
	}
	cfg, err = load(t, "[profiles.p]\nask_bell = true\n")
	if err != nil || !cfg.AskBell {
		t.Errorf("profile p on, the top level off: %v, %v", cfg.AskBell, err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ProjectFile), []byte("ask_bell = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Project(Default(), root); err == nil || !strings.Contains(err.Error(), `key "ask_bell" is not allowed in a project config`) {
		t.Errorf("in a project: %v", err)
	}
}
