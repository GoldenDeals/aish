package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runIn runs aish with args over config.toml body and returns the exit
// code and what went to stderr.
func runIn(t *testing.T, body, envProfile string, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", path)
	t.Setenv("AISH_PROFILE", envProfile)
	t.Setenv("AISH_MODEL", "")
	t.Setenv("AISH_EFFORT", "")
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code := run(args)
	os.Stdout, os.Stderr = oldOut, oldErr
	out, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(out)
}

func TestRunGoneProfile(t *testing.T) {
	for _, tc := range []struct {
		name, body, env string
	}{
		{"profile key", "model = \"top\"\nprofile = \"gone\"\n", ""},
		{"profile key, other tables", "profile = \"gone\"\n[profiles.work]\nmodel = \"w\"\n", ""},
		{"$AISH_PROFILE", "[profiles.work]\nmodel = \"w\"\n", "gone"},
	} {
		code, stderr := runIn(t, tc.body, tc.env, "help")
		if code != 0 {
			t.Errorf("%s: exit %d, stderr %q", tc.name, code, stderr)
		}
		if !strings.Contains(stderr, `no profile "gone"`) || !strings.Contains(stderr, "on the top level of config.toml") {
			t.Errorf("%s: stderr %q", tc.name, stderr)
		}
	}
}

// aish agent runs on each request and after each of its commands; the
// proxy, whose agent it is, reads config.toml itself.
func TestRunGoneProfileAgent(t *testing.T) {
	code, stderr := runIn(t, "profile = \"gone\"\n", "", "agent")
	if code == 0 || !strings.Contains(stderr, "usage: aish agent") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(stderr, "gone") {
		t.Errorf("stderr %q", stderr)
	}
}

// What is wrong with the file itself the top level has too: no going on
// with the defaults.
func TestRunBrokenConfig(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"syntax", "profile = \"gone\"\nmodel = \n"},
		{"unknown key", "profile = \"gone\"\nmodle = \"x\"\n"},
		{"negative limit", "profile = \"gone\"\nmax_steps = -1\n"},
		{"bad profile", "profile = \"gone\"\n[profiles.root]\nmodel = \"r\"\n"},
	} {
		code, stderr := runIn(t, tc.body, "", "help")
		if code == 0 {
			t.Errorf("%s: exit 0, stderr %q", tc.name, stderr)
		}
		if strings.Contains(stderr, "on the top level of config.toml") {
			t.Errorf("%s: stderr %q", tc.name, stderr)
		}
	}
}
