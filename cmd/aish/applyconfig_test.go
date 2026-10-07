package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/rpc"
)

func TestAppliedText(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, tc := range []struct {
		name string
		res  rpc.Applied
		want string
	}{
		{"nothing", rpc.Applied{}, "applied; nothing changed since the config was read\n"},
		{"keys and files", rpc.Applied{
			Keys:  []string{"fold_lines", "profiles.work.model"},
			Files: []string{filepath.Join(home, "src", "x", ".aish.toml")},
		}, "applied config.toml: fold_lines, profiles.work.model\napplied ~/src/x/.aish.toml\n"},
		{"switched", rpc.Applied{
			Keys:     []string{"profile"},
			Switched: true,
			Info:     rpc.Info{Profile: "local", Model: "qwen3:8b", Effort: "low"},
		}, "applied config.toml: profile\nprofile local, model qwen3:8b, effort low for this shell\n"},
		{"restart", rpc.Applied{
			Keys:    []string{"shell"},
			Restart: []string{"shell", filepath.Join(home, ".config", "aish", "mcp.yaml")},
		}, "applied config.toml: shell\n\x1b[33mrestart aish to apply shell, ~/.config/aish/mcp.yaml\x1b[0m\n"},
	} {
		if got := appliedText(tc.res); got != tc.want {
			t.Errorf("%s:\n%q\nwant\n%q", tc.name, got, tc.want)
		}
	}
}

// Outside aish there is no proxy to apply the config to.
func TestApplyConfigOutside(t *testing.T) {
	t.Setenv("AISH_SOCK", "")
	if code := applyConfigCmd(nil); code == 0 {
		t.Error("applied outside aish")
	}
	if code := applyConfigCmd([]string{"now"}); code == 0 {
		t.Error("an argument taken")
	}
}
