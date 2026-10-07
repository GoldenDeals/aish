package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPolicyHints(t *testing.T) {
	cfg, err := load(t, `[policy]
deny = ["sudo *", "git push*"]
ask  = ["apt install *"]
write_outside_home = "deny"
write_outside_home_hint = "write only under $HOME"

[policy.hints]
"sudo *" = "sudo is forbidden: use doas"
"apt install *" = """
ask the user
first"""
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"sudo *": "sudo is forbidden: use doas", "apt install *": "ask the user\nfirst"}
	if !reflect.DeepEqual(cfg.Policy.Hints, want) || cfg.Policy.WriteOutsideHomeHint != "write only under $HOME" {
		t.Errorf("policy %+v", cfg.Policy)
	}
}

// A hint that explains no rule is an error with the key named: a typo in
// its pattern must not leave the rule without the text quietly.
func TestPolicyHintErrors(t *testing.T) {
	for _, c := range []struct{ toml, want string }{
		{"[policy]\ndeny = [\"sudo *\"]\n[policy.hints]\n\"sudo*\" = \"x\"\n", `policy.hints["sudo*"]: no such pattern in deny or ask`},
		{"[policy]\nhints = { \"rm *\" = \"x\" }\n", `policy.hints["rm *"]: no such pattern in deny or ask`},
		{"[policy]\nask = [\"rm *\"]\nhints = { \"rm *\" = \"\" }\n", `policy.hints["rm *"]: empty hint`},
		{"[policy]\nask = [\"rm *\"]\nhints = { \"rm *\" = \" \\n\" }\n", `policy.hints["rm *"]: empty hint`},
		{"[policy]\nwrite_outside_home_hint = \"x\"\n", "policy.write_outside_home_hint: no write_outside_home"},
		{"[policy]\nwrite_outside_home = \"allow\"\nwrite_outside_home_hint = \"x\"\n", "policy.write_outside_home_hint: no write_outside_home"},
		{"[policy]\nwrite_outside_home = \"ask\"\nwrite_outside_home_hint = \"  \"\n", "policy.write_outside_home_hint: empty hint"},
	} {
		_, err := load(t, c.toml)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.toml, err, c.want)
		}
	}
}

// The project's hints are added to the global ones, both texts for a
// pattern both explain; the project may explain a global pattern; its
// write_outside_home_hint holds where its write_outside_home does.
func TestProjectPolicyHints(t *testing.T) {
	global := Policy{
		Deny:                 []string{"sudo *", "git push*"},
		WriteOutsideHome:     "ask",
		Hints:                map[string]string{"sudo *": "use doas", "git push*": "never push"},
		WriteOutsideHomeHint: "ask before writing outside $HOME",
	}
	for _, c := range []struct {
		name, project string
		hints         map[string]string
		writeHint     string
	}{
		{
			name:      "other patterns",
			project:   "[policy]\ndeny = [\"make deploy*\"]\n[policy.hints]\n\"make deploy*\" = \"CI deploys\"\n",
			hints:     map[string]string{"sudo *": "use doas", "git push*": "never push", "make deploy*": "CI deploys"},
			writeHint: "ask before writing outside $HOME",
		},
		{
			name:      "one pattern",
			project:   "[policy]\ndeny = [\"sudo *\"]\n[policy.hints]\n\"sudo *\" = \"or tell the user\"\n",
			hints:     map[string]string{"sudo *": "use doas or tell the user", "git push*": "never push"},
			writeHint: "ask before writing outside $HOME",
		},
		{
			name:      "a global pattern",
			project:   "[policy.hints]\n\"git push*\" = \"not from this repository\"\n",
			hints:     map[string]string{"sudo *": "use doas", "git push*": "never push not from this repository"},
			writeHint: "ask before writing outside $HOME",
		},
		{
			name:      "stricter write_outside_home",
			project:   "[policy]\nwrite_outside_home = \"deny\"\nwrite_outside_home_hint = \"build in ./out\"\n",
			hints:     global.Hints,
			writeHint: "build in ./out",
		},
		{
			name:      "alike write_outside_home",
			project:   "[policy]\nwrite_outside_home = \"ask\"\nwrite_outside_home_hint = \"build in ./out\"\n",
			hints:     global.Hints,
			writeHint: "ask before writing outside $HOME build in ./out",
		},
		{
			name:      "stricter without a hint",
			project:   "[policy]\nwrite_outside_home = \"deny\"\n",
			hints:     global.Hints,
			writeHint: "ask before writing outside $HOME",
		},
	} {
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, c.project)
		base := Default()
		base.Policy = global
		cfg, _, err := Project(base, root)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(cfg.Policy.Hints, c.hints) || cfg.Policy.WriteOutsideHomeHint != c.writeHint {
			t.Errorf("%s: hints %q, %q; want %q, %q", c.name, cfg.Policy.Hints, cfg.Policy.WriteOutsideHomeHint, c.hints, c.writeHint)
		}
		if !reflect.DeepEqual(global.Hints, map[string]string{"sudo *": "use doas", "git push*": "never push"}) {
			t.Fatalf("%s: the global hints changed: %q", c.name, global.Hints)
		}
	}
}

// The checks hold after a project's [policy] too, with the file named.
func TestProjectPolicyHintErrors(t *testing.T) {
	global := Policy{Deny: []string{"sudo *"}, WriteOutsideHome: "deny"}
	for _, c := range []struct{ project, want string }{
		{"[policy.hints]\n\"rm *\" = \"use trash\"\n", `policy.hints["rm *"]: no such pattern in deny or ask`},
		{"[policy]\ndeny = [\"rm *\"]\n[policy.hints]\n\"rm *\" = \"\"\n", `policy.hints["rm *"]: empty hint`},
		// The global write_outside_home is in force, not one this file
		// explains.
		{"[policy]\nwrite_outside_home_hint = \"stay home\"\n", "policy.write_outside_home_hint: no write_outside_home"},
	} {
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, c.project)
		base := Default()
		base.Policy = global
		_, _, err := Project(base, root)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), ProjectFile) {
			t.Errorf("%q: %v, want %q", c.project, err, c.want)
		}
	}
}
