package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

func TestUntrustedNote(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	project := filepath.Join(h, "repo", config.ProjectFile)

	cfg := config.Default()
	if s := untrustedNote(cfg, project); s != "" {
		t.Errorf("a trusted file gets a note: %q", s)
	}

	cfg.Untrusted = []string{"tools_dir"}
	s := untrustedNote(cfg, project)
	for _, want := range []string{"~/repo/" + config.ProjectFile, "tools_dir", "aish trust"} {
		if !strings.Contains(s, want) {
			t.Errorf("note %q does not have %q", s, want)
		}
	}
	if strings.Contains(s, "hooks_dir") {
		t.Errorf("note %q names a key the file is trusted with", s)
	}

	cfg.Untrusted = []string{"hooks_dir", "tools_dir"}
	if s := untrustedNote(cfg, project); !strings.Contains(s, "hooks_dir, tools_dir") {
		t.Errorf("note %q does not name both keys", s)
	}
}
