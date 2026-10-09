package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/policy"
)

// The built-in policy leads the line of aish policy and aish status, by
// its count of policies, apart from the files of policy_dir.
func TestPolicyLineBuiltin(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	empty := filepath.Join(root, "empty")
	cedar := filepath.Join(root, "cedar")
	for _, d := range []string{empty, cedar} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cedar, "x.cedar"), []byte("permit(principal, action, resource);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dir   string
		rules policy.Rules
		want  string
	}{
		{empty, policy.Rules{Builtin: true}, "built-in (12), no policies in ~/empty"},
		{cedar, policy.Rules{Builtin: true, Deny: []string{"sudo *"}},
			"built-in (12), 1 policy in ~/cedar: x.cedar (1) + 1 rule from config.toml"},
		{cedar, policy.Rules{}, "1 policy in ~/cedar: x.cedar (1)"},
	} {
		eng, err := policy.Load(context.Background(), tc.dir, tc.rules)
		if err != nil {
			t.Fatal(err)
		}
		if got := policyLine(eng.Summary(), tc.dir, tc.rules.Len(), tc.rules.Len(), ""); got != tc.want {
			t.Errorf("policyLine:\n got %q\nwant %q", got, tc.want)
		}
	}
}

// aish policy shows the built-in policy of the config, and --builtin
// prints its text, which loads from policy_dir as it is.
func TestPolicyBuiltinCmd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "home", ".config"))
	t.Setenv("AISH_SOCK", "") // not the proxy of the shell running the tests
	t.Chdir(root)
	dir := filepath.Join(root, "policy")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, out := policyIn(t, dir); code != 0 || !strings.HasPrefix(out, "built-in (12), no policies in ") {
		t.Errorf("aish policy: %d, %q", code, out)
	}
	code, out := policyIn(t, dir, "--builtin")
	if code != 0 || out != strings.TrimSpace(policy.BuiltinText) {
		t.Fatalf("aish policy --builtin: %d, %q", code, out)
	}
	if code, _ := policyIn(t, dir, "--builtin", "x"); code == 0 {
		t.Error("aish policy --builtin x passed")
	}
	if err := os.WriteFile(filepath.Join(dir, "copy.cedar"), []byte(out+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := policyIn(t, dir, "bash", "rm -rf /"); code != 1 || out != "deny: wipes /, a top-level directory or $HOME" {
		t.Errorf("aish policy bash 'rm -rf /' with the copy: %d, %q", code, out)
	}
	if code, out := policyIn(t, dir); code != 0 || !strings.Contains(out, "copy.cedar (12)") {
		t.Errorf("aish policy with the copy: %d, %q", code, out)
	}
}
