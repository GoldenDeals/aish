package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

func TestTrustCmd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	cfg := config.Default()
	if trustCmd(cfg, nil) == 0 {
		t.Error("trusted with no project file")
	}

	file := filepath.Join(repo, config.ProjectFile)
	if err := os.WriteFile(file, []byte("hooks_dir = \".aish/hooks\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if trustCmd(cfg, []string{"--all"}) == 0 || config.Trusted(file) {
		t.Error("an unknown flag is taken")
	}
	if trustCmd(cfg, nil) != 0 || !config.Trusted(file) {
		t.Fatal("the project file of the directory is not trusted")
	}
	if trustCmd(cfg, []string{"--list"}) != 0 {
		t.Error("--list failed")
	}
	if trustCmd(cfg, []string{"--revoke"}) != 0 || config.Trusted(file) {
		t.Error("--revoke left the file trusted")
	}

	// A file aish refuses to take is not trusted either.
	if err := os.WriteFile(file, []byte("model = \"x\"\nhooks_dir = \".aish/hooks\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if trustCmd(cfg, nil) == 0 || config.Trusted(file) {
		t.Error("a file with a key not allowed is trusted")
	}
}
