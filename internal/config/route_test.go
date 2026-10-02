package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRoute(t *testing.T) {
	cfg, err := load(t, "[route]\nnot_found = false\nsuffix = \"\"\n")
	if err != nil {
		t.Fatal(err)
	}
	// The keys left out keep their defaults.
	if want := (Route{Capital: true, MinWords: 2, Expand: true}); cfg.Route != want {
		t.Errorf("route %+v, want %+v", cfg.Route, want)
	}
	for _, tc := range []struct{ toml, want string }{
		{"[route]\nmin_words = -1\n", "route.min_words = -1: must not be negative"},
		{"[route]\nsuffix = \"?\\n\"\n", `route.suffix "?\n": must be one line`},
		{"[route]\nnotfound = true\n", `unknown key "route.notfound"`},
	} {
		if _, err := load(t, tc.toml); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.toml, err, tc.want)
		}
	}
}

// The shell reads [route] when aish starts it, not per request: a project
// cannot set it.
func TestProjectRoute(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	repo(t, filepath.Join(root, "x"), "[route]\ncapital = false\n")
	if _, _, err := Project(Default(), filepath.Join(root, "x")); err == nil || !strings.Contains(err.Error(), "not allowed in a project config") {
		t.Errorf("[route] in a project: %v", err)
	}
}
