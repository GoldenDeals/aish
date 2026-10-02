package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// The hooks of a cloned repository do not run until its .aish.toml is
// trusted, as it is; the proxy says so once per file and contents.
func TestUntrustedProject(t *testing.T) {
	var replies []*llm.Response
	for range 5 {
		replies = append(replies, &llm.Response{Text: "ok"})
	}
	p, out, cwd := hosted(t, &scripted{replies: replies})
	t.Setenv("XDG_DATA_HOME", filepath.Join(filepath.Dir(cwd), "data"))
	hook := filepath.Join(cwd, ".aish", "hooks", "user-prompt", "mark")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho ran >>ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cwd, config.ProjectFile)
	write := func(s string) {
		if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ask := func() (ran bool) {
		t.Helper()
		os.Remove(filepath.Join(cwd, "ran"))
		p.marker(Marker{Kind: "ask-start"})
		if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hi", Cwd: cwd}); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(cwd, "ran"))
		return err == nil
	}
	const line = "aish: ~/work/.aish.toml sets hooks_dir; they run code from the repository, so aish skips them until you run aish trust"
	told := func() int { return strings.Count(out.String(), line) }

	write("hooks_dir = \".aish/hooks\"\n")
	if ask() {
		t.Error("the hook of an untrusted file ran")
	}
	if told() != 1 {
		t.Errorf("not told once: %q", out.String())
	}
	if ask() || told() != 1 {
		t.Errorf("second request: told %d times", told())
	}

	if err := config.Trust(file); err != nil {
		t.Fatal(err)
	}
	if !ask() {
		t.Error("the hook of a trusted file did not run")
	}

	// An edit takes the trust back, and is told of anew.
	write("hooks_dir = \".aish/hooks\"\nmax_steps = 9\n")
	if ask() {
		t.Error("the hook ran after an edit")
	}
	if told() != 2 {
		t.Errorf("the edited file not told of: %d times", told())
	}
}
