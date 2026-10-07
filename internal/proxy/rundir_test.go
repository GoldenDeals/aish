package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// TestMakeRunDir: bin gets aish, for the processes the shell runs, and only
// when PATH has none; no tool or subcommand becomes a command of the shell.
func TestMakeRunDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	path := filepath.Join(dir, "path")
	os.MkdirAll(path, 0o755)
	t.Setenv("PATH", path)

	got, err := makeRunDir("/opt/aish", "n", config.Route{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(got)
	bin := filepath.Join(got, "bin")
	if names := binNames(t, bin); len(names) != 1 || names[0] != "aish" {
		t.Errorf("bin without aish on PATH: %q, want just aish", names)
	}
	b, err := os.ReadFile(filepath.Join(bin, "aish"))
	if want := `exec "/opt/aish" "$@"`; err != nil || !strings.Contains(string(b), want) {
		t.Errorf("aish: %q, %v; want %q", b, err, want)
	}
	if st, err := os.Stat(filepath.Join(bin, "aish")); err != nil || st.Mode()&0o111 == 0 {
		t.Errorf("aish: not executable (%v)", err)
	}
	if b, err := os.ReadFile(filepath.Join(got, "nonce")); err != nil || string(b) != "n\n" {
		t.Errorf("nonce: %q, %v", b, err)
	}

	// With aish on PATH, bin stays empty.
	os.WriteFile(filepath.Join(path, "aish"), []byte("#!/bin/sh\n"), 0o755)
	got2, err := makeRunDir("/opt/aish", "n", config.Route{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(got2)
	if names := binNames(t, filepath.Join(got2, "bin")); len(names) != 0 {
		t.Errorf("bin with aish on PATH: %q, want it empty", names)
	}
}

func binNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}
