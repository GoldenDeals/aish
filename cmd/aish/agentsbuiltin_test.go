package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// aish agents shows aish's own subagents as built-in, Explore with the
// tools it is limited to; a file of the same name takes the place of one.
func TestAgentsBuiltin(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	code, out, _ := captured(t, func() int { return agentsCmd(config.Default(), nil) })
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines %q", lines)
	}
	if !regexp.MustCompile(`^\x1b\[1mExplore\x1b\[0m +built-in +inherit +tools: Read, Grep, Glob, LS +Read-only`).MatchString(lines[0]) {
		t.Errorf("Explore: %q", lines[0])
	}
	if !regexp.MustCompile(`^\x1b\[1mgeneral-purpose\x1b\[0m +built-in +inherit +General-purpose`).MatchString(lines[1]) {
		t.Errorf("general-purpose: %q", lines[1])
	}

	dir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Explore.md"), []byte("---\ndescription: Mine\ntools: Read\n---\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, _ = captured(t, func() int { return agentsCmd(config.Default(), nil) })
	lines = strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 || !regexp.MustCompile(`^\x1b\[1mExplore\x1b\[0m +~/\.claude +inherit +tools: Read +Mine$`).MatchString(lines[0]) {
		t.Errorf("Explore of a file: %q", lines)
	}
}
