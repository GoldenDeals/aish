package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/tools"
)

// The shell the request comes from (tools.Exec.Shell) is the one the
// policy reads the commands in: to bash `noglob rm -rf x` is a command
// named noglob, to zsh it runs rm, and a line zsh reads otherwise than
// bash is not handed off on the strength of bash's reading.
func TestPolicyZsh(t *testing.T) {
	pol, err := policy.Load(context.Background(), "", policy.Rules{Deny: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		shell  string
		handed bool
	}{{"", true}, {"bash", true}, {"zsh", false}} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"noglob rm -rf x"}`)}},
			{Text: "done"},
		}}
		a, j, sh, _, cwd := newAgent(t, prov)
		a.Policy, a.Cfg.HooksDir = pol, ""
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd, Shell: tc.shell, Opts: []string{"equals"}}); err != nil {
			t.Fatal(err)
		}
		if got := len(sh.handed) == 1; got != tc.handed {
			t.Errorf("shell %q: handed off %q, journal %s", tc.shell, sh.handed, kinds(j.es))
		}
	}
}

func TestEnvironmentShell(t *testing.T) {
	if env := environment("zsh", "m", "", "u"); !strings.Contains(env, "- Shell: zsh") {
		t.Errorf("zsh:\n%s", env)
	}
	if env := environment("", "m", "", "u"); !strings.Contains(env, "- Shell: bash") {
		t.Errorf("bash:\n%s", env)
	}
}
