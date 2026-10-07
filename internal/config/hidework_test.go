package config

import (
	"path/filepath"
	"testing"
)

// hide_work is off unless set; set in config.toml it holds under any
// profile, which has no key of its own for it, and a project file sets it
// either way, as fold_lines.
func TestHideWork(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	if Default().HideWork {
		t.Fatal("hide_work on by default")
	}
	cfg, err := load(t, "model = \"m\"\n")
	if err != nil || cfg.HideWork {
		t.Fatalf("not set: %v, err %v", cfg.HideWork, err)
	}
	cfg, err = load(t, "hide_work = true\nprofile = \"work\"\n\n[profiles.work]\nmodel = \"w\"\n")
	if err != nil || !cfg.HideWork || cfg.Profile != "work" {
		t.Fatalf("config.toml with its profile: hide_work %v, profile %q, err %v", cfg.HideWork, cfg.Profile, err)
	}
	for _, name := range []string{"work", ""} {
		if cfg, err := LoadProfile(name); err != nil || !cfg.HideWork {
			t.Errorf("profile %q: hide_work %v, err %v", name, cfg.HideWork, err)
		}
	}
	if _, err := load(t, "[profiles.work]\nhide_work = true\n"); err == nil {
		t.Error("hide_work taken inside a profile")
	}

	root := trustHome(t)
	for _, tc := range []struct {
		global bool
		file   string
		want   bool
	}{
		{false, "hide_work = true\n", true},
		{true, "hide_work = false\n", false},
		{true, "max_steps = 3\n", true},
	} {
		dir := filepath.Join(root, "home", "src", "x")
		repo(t, dir, tc.file)
		base := Default()
		base.HideWork = tc.global
		cfg, file, err := Project(base, dir)
		if err != nil || file == "" || cfg.HideWork != tc.want {
			t.Errorf("config.toml %v, %s: hide_work %v, file %q, err %v; want %v", tc.global, tc.file, cfg.HideWork, file, err, tc.want)
		}
	}
}
