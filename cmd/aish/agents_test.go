package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// aish agents shows what a file takes away besides tools, its mode (one
// that would lift checks with a note that it does not), and the fields
// aish does not read, on a line of their own that is not a problem.
func TestAgentsLimits(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	dir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"nobash": "---\nname: nobash\ndescription: Reads only\ndisallowedTools: Bash, Write, Edit\npermissionMode: plan\n" +
			"hooks:\n  Stop: []\n---\nBody.\n",
		"root":  "---\nname: root\ndescription: Asks for all\ntools: Read\npermissionMode: bypassPermissions\n---\nBody.\n",
		"plain": "---\nname: plain\ndescription: Nothing\n---\nBody.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := captured(t, func() int { return agentsCmd(config.Default(), nil) })
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	// aish's own, Explore and general-purpose, sort before the files'.
	if len(lines) != 6 {
		t.Fatalf("lines %q", lines)
	}
	lines = lines[2:]
	if l := lines[0]; !strings.Contains(l, "disallowed: Bash, Write, Edit  mode: plan") || strings.Contains(l, "tools:") {
		t.Errorf("nobash: %q", l)
	}
	if l := lines[1]; l != "\x1b[2m  ignored: hooks\x1b[0m" {
		t.Errorf("nobash's ignored fields: %q", l)
	}
	if l := lines[2]; !strings.Contains(l, "plain") || strings.Contains(l, "mode:") || strings.Contains(l, "disallowed:") {
		t.Errorf("plain: %q", l)
	}
	if l := lines[3]; !strings.Contains(l, "tools: Read  mode: bypassPermissions (no effect)") {
		t.Errorf("root: %q", l)
	}
	if strings.Contains(out, "problem") {
		t.Errorf("a problem: %s", out)
	}
}
