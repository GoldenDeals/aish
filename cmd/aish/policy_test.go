package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/policy"
)

func TestPolicyLine(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	empty := filepath.Join(root, "empty")
	cedar := filepath.Join(root, "cedar")
	one := filepath.Join(root, "one")
	for _, d := range []string{empty, cedar, one} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := "permit(principal, action, resource);\n" +
		"forbid(principal, action == Action::\"run\", resource == Command::\"sudo\");\n"
	if err := os.WriteFile(filepath.Join(cedar, "x.cedar"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(one, "y.cedar"), []byte("permit(principal, action, resource);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "proj", ".aish.toml")

	for _, tc := range []struct {
		name    string
		dir     string
		rules   policy.Rules
		global  int
		project string
		want    string
	}{
		{"nothing", empty, policy.Rules{}, 0, "", "no policies in ~/empty"},
		{"cedar", cedar, policy.Rules{}, 0, "", "2 policies in ~/cedar: x.cedar (2)"},
		{"one policy", one, policy.Rules{}, 0, "", "1 policy in ~/one: y.cedar (1)"},
		{"one global rule", empty, policy.Rules{Deny: []string{"sudo *"}}, 1, "",
			"no policies in ~/empty + 1 rule from config.toml"},
		{"global and project rules", empty,
			policy.Rules{Deny: []string{"sudo *", "rm -rf *", "dd *"}, Ask: []string{"git push*"}, WriteOutsideHome: policy.Ask},
			3, project,
			"no policies in ~/empty + 3 rules from config.toml + 2 rules from ~/proj/.aish.toml"},
		{"dir list", empty + string(filepath.ListSeparator) + cedar, policy.Rules{}, 0, "",
			"2 policies in ~/empty, ~/cedar: x.cedar (2)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := policy.Load(context.Background(), tc.dir, tc.rules)
			if err != nil {
				t.Fatal(err)
			}
			if got := policyLine(eng, tc.dir, tc.global, tc.rules.Len(), tc.project); got != tc.want {
				t.Errorf("policyLine:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}
