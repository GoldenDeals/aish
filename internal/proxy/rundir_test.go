package proxy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/tools"
)

// wrapped is a tool of a kind makeRunDir does not know, which asks for a
// wrapper.
type wrapped string

func (w wrapped) Name() string         { return string(w) }
func (wrapped) Desc() string           { return "" }
func (wrapped) Args() []tools.Arg      { return nil }
func (wrapped) Schema() map[string]any { return tools.Schema(nil) }
func (wrapped) Wrapper() bool          { return true }
func (wrapped) Execute(context.Context, tools.Exec, map[string]any, io.Writer) (string, error) {
	return "", nil
}

func TestMakeRunDir(t *testing.T) {
	dir := t.TempDir()
	// `expand` stands for a subcommand that is a command already.
	path := filepath.Join(dir, "path")
	os.MkdirAll(path, 0o755)
	os.WriteFile(filepath.Join(path, "expand"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", path)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	reg := tools.Load(filepath.Join(dir, "none"))
	reg.Add(wrapped("status"))  // a tool named like a subcommand
	reg.Add(wrapped("weather")) // an ordinary one
	reg.Add(wrapped("expand"))

	got, err := makeRunDir(reg, []string{"status", "model", "expand"}, "/opt/aish", "n", config.Route{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(got)
	bin := filepath.Join(got, "bin")
	for name, want := range map[string]string{
		"status":  `exec "/opt/aish" status "$@"`, // the subcommand, not the tool
		"model":   `exec "/opt/aish" model "$@"`,
		"weather": `exec "/opt/aish" tool weather "$@"`,
		// A built-in: the user may type it, too.
		"read_file": `exec "/opt/aish" tool read_file "$@"`,
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
	if _, err := os.Stat(filepath.Join(bin, "bash")); err == nil {
		t.Error("bash: a wrapper")
	}
	if b, err := os.ReadFile(filepath.Join(got, "nonce")); err != nil || string(b) != "n\n" {
		t.Errorf("nonce: %q, %v", b, err)
	}
}
