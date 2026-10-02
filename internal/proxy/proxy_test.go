package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBashPath(t *testing.T) {
	dir := t.TempDir()
	mine, other := filepath.Join(dir, "opt", "bash"), filepath.Join(dir, "bin", "bash")
	zsh := filepath.Join(dir, "bin", "zsh")
	for _, p := range []string{mine, other, zsh} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("PATH", filepath.Dir(other))

	for _, tc := range []struct{ shell, configured, want string }{
		{mine, "", mine},      // the login shell, though not first in PATH
		{zsh, "", other},      // not a bash: PATH's
		{"", "", other},       // no $SHELL
		{zsh, mine, mine},     // configured
		{mine, "bash", other}, // configured by name
		{filepath.Join(dir, "gone", "bash"), "", other},
	} {
		t.Setenv("SHELL", tc.shell)
		if got, err := bashPath(tc.configured); got != tc.want || err != nil {
			t.Errorf("SHELL=%s shell=%q: %s, %v; want %s", tc.shell, tc.configured, got, err, tc.want)
		}
	}
	if _, err := bashPath(filepath.Join(dir, "missing")); err == nil {
		t.Error("a configured shell that is not there: no error")
	}
}
