package config

import (
	"path/filepath"
	"testing"
)

// The built-in policy is on unless config.toml turns it off, whatever
// else [policy] holds.
func TestLoadBuiltinPolicy(t *testing.T) {
	for _, tc := range []struct {
		toml string
		want bool
	}{
		{"", true},
		{"[policy]\ndeny = [\"sudo *\"]\n", true},
		{"[policy]\nbuiltin = true\n", true},
		{"[policy]\nbuiltin = false\n", false},
	} {
		cfg, err := load(t, tc.toml)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Policy.Builtin != tc.want {
			t.Errorf("%q: builtin %v, want %v", tc.toml, cfg.Policy.Builtin, tc.want)
		}
	}
}

// A project may turn the built-in policy on, not off: a cloned repository
// must not take the user's safety net away.
func TestProjectBuiltinPolicy(t *testing.T) {
	for _, tc := range []struct {
		global  bool
		project string
		want    bool
	}{
		{true, "[policy]\nbuiltin = false\n", true},
		{true, "[policy]\ndeny = [\"x *\"]\n", true},
		{false, "[policy]\nbuiltin = true\n", true},
		{false, "[policy]\nbuiltin = false\n", false},
		{false, "[policy]\ndeny = [\"x *\"]\n", false},
		{false, "max_steps = 3\n", false},
	} {
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, tc.project)
		base := Default()
		base.Policy.Builtin = tc.global
		cfg, path, err := Project(base, root)
		if err != nil || path == "" {
			t.Fatalf("%q: %q, %v", tc.project, path, err)
		}
		if cfg.Policy.Builtin != tc.want {
			t.Errorf("global %v, project %q: builtin %v, want %v", tc.global, tc.project, cfg.Policy.Builtin, tc.want)
		}
	}
}
