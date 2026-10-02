package proxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/tools"
)

func TestMakeRunDir(t *testing.T) {
	dir := t.TempDir()
	// `expand` stands for a subcommand that is a command already.
	path := filepath.Join(dir, "path")
	os.MkdirAll(path, 0o755)
	os.WriteFile(filepath.Join(path, "expand"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", path)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	run := func(name string) tools.Tool {
		return tools.Tool{Name: name, Run: func(context.Context, map[string]any) (string, error) { return "", nil }}
	}
	reg := tools.Load(filepath.Join(dir, "none"))
	reg.Add(run("status"))  // a skill named like a subcommand
	reg.Add(run("weather")) // an ordinary one
	reg.Add(run("expand"))

	got, err := makeRunDir(reg, []string{"status", "model", "expand"}, "/opt/aish", "n")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(got)
	bin := filepath.Join(got, "bin")
	for name, want := range map[string]string{
		"status":  `exec "/opt/aish" status "$@"`, // the subcommand, not the tool
		"model":   `exec "/opt/aish" model "$@"`,
		"weather": `exec "/opt/aish" tool weather "$@"`,
	} {
		b, err := os.ReadFile(filepath.Join(bin, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("%s: %q, want %q", name, b, want)
		}
		if st, _ := os.Stat(filepath.Join(bin, name)); st.Mode()&0o111 == 0 {
			t.Errorf("%s: not executable", name)
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "expand")); err == nil {
		t.Error("expand: a wrapper over the command on PATH")
	}
	if _, err := os.Stat(filepath.Join(bin, tools.Bash)); err == nil {
		t.Error("bash: a wrapper")
	}
	if b, err := os.ReadFile(filepath.Join(got, "nonce")); err != nil || string(b) != "n\n" {
		t.Errorf("nonce: %q, %v", b, err)
	}
}
