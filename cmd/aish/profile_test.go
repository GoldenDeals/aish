package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/rpc"
)

func TestProfileArg(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = map[string]config.Profile{"local": {}, "high": {}}
	for _, tc := range []struct {
		args, rest []string
		profile    string
		ok         bool
	}{
		{nil, nil, "", false},
		{[]string{"local"}, []string{}, "local", true},
		{[]string{"local", "qwen3:8b", "high"}, []string{"qwen3:8b", "high"}, "local", true},
		// A profile goes before an effort, or a model, of its name.
		{[]string{"high"}, []string{}, "high", true},
		{[]string{"local", "local"}, []string{"local"}, "local", true},
		{[]string{"claude-x", "local"}, []string{"claude-x", "local"}, "", false},
		// The top level, which no profile can be named.
		{[]string{"root"}, []string{}, "", true},
		{[]string{"root", "qwen3:8b"}, []string{"qwen3:8b"}, "", true},
	} {
		profile, rest, ok := profileArg(cfg, tc.args)
		if profile != tc.profile || ok != tc.ok || !slices.Equal(rest, tc.rest) {
			t.Errorf("%q: %q %q %v", tc.args, profile, rest, ok)
		}
	}
}

func TestProfileOf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := "model = \"top\"\nprofile = \"work\"\n[profiles.work]\nmodel = \"w\"\n[profiles.local]\nmodel = \"l\"\n"
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", path)
	t.Setenv("AISH_PROFILE", "")
	t.Setenv("AISH_MODEL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		info          rpc.Info
		profile, want string
		err           bool
	}{
		{rpc.Info{Profile: "work", Model: "x"}, "work", "w", false},
		{rpc.Info{Profile: "local", Model: "x"}, "local", "l", false},
		{rpc.Info{Model: "x"}, "", "top", false},
		{rpc.Info{}, "work", "w", false}, // a proxy older than aish model
		{rpc.Info{Profile: "gone", Model: "x"}, "work", "w", true},
	} {
		got, err := profileOf(cfg, tc.info)
		if got.Profile != tc.profile || got.Model != tc.want || (err != nil) != tc.err {
			t.Errorf("%+v: %q %q %v", tc.info, got.Profile, got.Model, err)
		}
	}
}
