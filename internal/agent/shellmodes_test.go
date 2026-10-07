package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/tools"
)

// The options the shell has on come with the request (tools.Exec.Opts):
// under set -k, git fetch GIT_SSH_COMMAND='sudo ls' has git run sudo, and
// the policy judges it so, the command of the model and one a pre-tool
// hook puts in its place alike. Without the option it is a plain argument.
func TestPolicyShellOptions(t *testing.T) {
	pol, err := policy.Load(context.Background(), "", policy.Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	const fetch = `{"command":"git fetch GIT_SSH_COMMAND='sudo ls'"}`
	const denied = `denied by policy: matches "sudo *"`
	for _, tc := range []struct {
		args, reply string
		opts        []string
		denied      bool
	}{
		{fetch, "", []string{"braceexpand", "keyword"}, true},
		{`{"command":"ls"}`, `{"args":` + fetch + `}`, []string{"keyword"}, true},
		{fetch, "", []string{"braceexpand", "cdable_vars"}, false},
		{fetch, "", nil, false},
	} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", tc.args)}},
			{Text: "done"},
		}}
		a, j, sh, _, cwd := newAgent(t, prov)
		a.Policy, a.Cfg.HooksDir = pol, ""
		if tc.reply != "" {
			hook(t, a, "pre-tool", "h", "cat >/dev/null; echo '"+strings.ReplaceAll(tc.reply, "'", `'\''`)+"'")
		}
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd, Opts: tc.opts}); err != nil {
			t.Fatal(err)
		}
		if !tc.denied {
			if len(sh.handed) != 1 {
				t.Errorf("%s in %q: handed off %q, journal %s", tc.args, tc.opts, sh.handed, kinds(j.es))
			}
			continue
		}
		if len(j.es) < 3 {
			t.Errorf("%s in %q: journal %s, handed off %q", tc.args, tc.opts, kinds(j.es), sh.handed)
			continue
		}
		if r := j.es[2]; r.ToolCallID != "c1" || !r.IsError || r.Output != denied {
			t.Errorf("%s in %q: result %+v", tc.args, tc.opts, r)
		}
		if len(sh.handed) != 0 {
			t.Errorf("%s in %q: handed off %q", tc.args, tc.opts, sh.handed)
		}
	}
}

// shopt -so keyword and a shell started under set -k are set -k to a
// subagent's patterns: NAME=value among the words of a command sets a
// variable. SHELLOPTS is a variable env sets.
func TestScopedShoptKeyword(t *testing.T) {
	dir := t.TempDir()
	s := &bashScope{patterns: []string{"git *", "shopt *", "set *", "bash *", "env *"}}
	for cmd, want := range map[string]string{
		"shopt -so keyword; git log":              "set -k",
		"shopt -s -o keyword; git log":            "set -k",
		"shopt -o -s keyword; git log":            "set -k",
		"set -o keyword; git log":                 "set -k",
		"bash -k -c 'git log GIT_DIR=x'":          "set -k",
		"bash -o keyword -c 'git log GIT_DIR=x'":  "set -k",
		"bash -eo keyword -c 'git log'":           "set -k",
		`bash -o "$o" -c 'git log'`:               "set -k",
		"env SHELLOPTS=keyword bash -c 'git log'": "sets SHELLOPTS",
	} {
		if why := refused(s, cmd, dir, nil); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q in it", cmd, why, want)
		}
	}
	for _, cmd := range []string{
		"shopt -s extglob; git log",
		"shopt -o keyword; git log",
		"shopt -u -o keyword; git log",
		"bash -c 'git log' -k",
		"bash +k -c 'git log'",
		"bash --rcfile /dev/null -O extglob -c 'git log'",
	} {
		if why := refused(s, cmd, dir, nil); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}
