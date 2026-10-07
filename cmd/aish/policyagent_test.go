package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// policyIn runs aish policy with args in a directory of its own over the
// policies of dir and returns the exit code and what went to stdout.
func policyIn(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	tmp := t.TempDir()
	files := map[string]**os.File{"stdin": &os.Stdin, "stdout": &os.Stdout, "stderr": &os.Stderr}
	for name, std := range files {
		f, err := os.OpenFile(filepath.Join(tmp, name), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		old := *std
		*std = f
		defer func() { *std = old }()
	}
	cfg := config.Default()
	cfg.PolicyDir = dir
	code := policyCmd(cfg, args)
	out, err := os.ReadFile(filepath.Join(tmp, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	return code, strings.TrimSpace(string(out))
}

// aish policy --agent NAME asks the policies about a call of subagent
// NAME: the rule of README for reviewer stops its write_file, and the same
// call without the flag is the host agent's, which passes.
func TestPolicyAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "home", ".config"))
	t.Setenv("AISH_SOCK", "") // not the proxy of the shell running the tests
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(work, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	dir := filepath.Join(root, "policy")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `permit(principal, action, resource);

@reason("reviewer only reads")
forbid(principal, action == Action::"write", resource)
when { context has agent && context.agent == "reviewer" };

@reason("reviewer only reads")
forbid(principal, action == Action::"run", resource)
when { context has agent && context.agent == "reviewer" &&
       !["cat", "grep", "head", "ls", "rg"].contains(context.program) };
`
	if err := os.WriteFile(filepath.Join(dir, "reviewer.cedar"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"--agent", "reviewer", "write_file", "x"}, 1, "deny: reviewer only reads"},
		{[]string{"write_file", "x"}, 0, "allow"},
		{[]string{"--agent=reviewer", "bash", "rm -rf build"}, 1, "deny: reviewer only reads"},
		{[]string{"--agent", "reviewer", "bash", "grep -rn x ."}, 0, "allow"},
		{[]string{"bash", "rm -rf build"}, 0, "allow"},
		{[]string{"--agent", "tester", "write_file", "x"}, 0, "allow"},
	} {
		code, out := policyIn(t, dir, tc.args...)
		if code != tc.code || out != tc.out {
			t.Errorf("aish policy %q: exit %d, %q; want %d, %q", tc.args, code, out, tc.code, tc.out)
		}
	}
	// A name is needed, and a call to check: --agent alone asks nothing.
	for _, args := range [][]string{{"--agent"}, {"--agent="}, {"--agent", "reviewer"}, {"--agent", "", "write_file", "x"}} {
		if code, out := policyIn(t, dir, args...); code == 0 || out != "" {
			t.Errorf("aish policy %q: exit %d, %q", args, code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "x")); err == nil {
		t.Error("aish policy wrote the file")
	}
}
