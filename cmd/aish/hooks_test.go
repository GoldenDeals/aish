package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/hooks"
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// writeHook puts a file at dir/event/name, executable or not by mode.
func writeHook(t *testing.T, dir, event, name string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, event), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, event, name), []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestListHooks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	user := filepath.Join(root, "user")
	proj := filepath.Join(root, "proj")
	// post-tool sorts before pre-tool, and the project's hook before the
	// user's by name: neither is the order they run in.
	writeHook(t, user, hooks.PostTool, "mask-secrets", 0o755)
	writeHook(t, user, hooks.PreTool, "20-ssh", 0o755)
	writeHook(t, user, hooks.PreTool, "10-no-sudo", 0o755)
	writeHook(t, user, hooks.Stop, "notify", 0o644)
	writeHook(t, proj, hooks.PreTool, "05-lint", 0o755)
	dirs := user + string(filepath.ListSeparator) + proj

	set, problems := hooks.Find(dirs)
	var b bytes.Buffer
	listHooks(&b, set, problems, dirs)
	want := "pre-tool\n" +
		"  10-no-sudo    ~/user/pre-tool/10-no-sudo\n" +
		"  20-ssh        ~/user/pre-tool/20-ssh\n" +
		"  05-lint       ~/proj/pre-tool/05-lint\n" +
		"post-tool\n" +
		"  mask-secrets  ~/user/post-tool/mask-secrets\n" +
		"problem: ~/user/stop/notify: not executable, skipped\n"
	if got := ansi.ReplaceAllString(b.String(), ""); got != want {
		t.Errorf("listHooks:\n got %q\nwant %q", got, want)
	}
	if got, want := hooksLine(dirs), "4 (pre-tool 3, post-tool 1) in ~/user, ~/proj; 1 problem, aish hooks"; got != want {
		t.Errorf("hooksLine:\n got %q\nwant %q", got, want)
	}

	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	set, problems = hooks.Find(empty)
	b.Reset()
	listHooks(&b, set, problems, empty)
	if got, want := b.String(), "no hooks in ~/empty\n"; got != want {
		t.Errorf("listHooks of an empty directory: got %q, want %q", got, want)
	}
	if got, want := hooksLine(empty), "none in ~/empty"; got != want {
		t.Errorf("hooksLine of an empty directory: got %q, want %q", got, want)
	}

	broken := filepath.Join(root, "broken")
	writeHook(t, broken, hooks.PreTool, "a", 0o644)
	writeHook(t, broken, "pre_tool", "b", 0o755)
	if got, want := hooksLine(broken), "none in ~/broken; 2 problems, aish hooks"; got != want {
		t.Errorf("hooksLine with problems only: got %q, want %q", got, want)
	}
}

// The hooks of a project file count only once it is trusted, and an
// untrusted one is named, as the agent would skip its hooks.
func TestPrintHooksTrust(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, config.ProjectFile)
	if err := os.WriteFile(file, []byte("hooks_dir = \".aish/hooks\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHook(t, filepath.Join(repo, ".aish", "hooks"), hooks.PreTool, "repo-guard", 0o755)
	cfg := config.Default()
	cfg.HooksDir = filepath.Join(root, "user")
	writeHook(t, cfg.HooksDir, hooks.PreTool, "no-sudo", 0o755)

	var b bytes.Buffer
	if a, err := onDisk(cfg, repo, nil, false); err != nil {
		t.Fatal(err)
	} else {
		printHooks(&b, a.cfg, a.project)
	}
	out := ansi.ReplaceAllString(b.String(), "")
	if !strings.Contains(out, "no-sudo") || strings.Contains(out, "repo-guard") {
		t.Errorf("untrusted: want the user's hook and not the project's:\n%s", out)
	}
	if !strings.Contains(out, file+" sets hooks_dir, but is not trusted: its hooks run after aish trust\n") {
		t.Errorf("untrusted: the project file is not named:\n%s", out)
	}

	if err := config.Trust(file); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if a, err := onDisk(cfg, repo, nil, false); err != nil {
		t.Fatal(err)
	} else {
		printHooks(&b, a.cfg, a.project)
	}
	out = ansi.ReplaceAllString(b.String(), "")
	if !strings.Contains(out, "  no-sudo     "+cfg.HooksDir) || !strings.Contains(out, "  repo-guard  "+repo) ||
		strings.Index(out, "no-sudo") > strings.Index(out, "repo-guard") {
		t.Errorf("trusted: want the user's hook, then the project's:\n%s", out)
	}
	if strings.Contains(out, "not trusted") {
		t.Errorf("trusted: the project file is still named untrusted:\n%s", out)
	}
}
