package agent

import (
	"context"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/tools"
)

// The global aliases of the zsh the request comes from go to the policy:
// with alias -g G='| sh', `echo 'rm -rf x' G` runs rm, and the line is
// not handed off on the strength of a reading without them.
func TestPolicyZshGlobalAliases(t *testing.T) {
	pol, err := policy.Load(context.Background(), "", policy.Rules{Deny: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		aliases []string
		handed  bool
	}{{nil, true}, {[]string{"L"}, true}, {[]string{"G"}, false}} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"echo 'rm -rf x' G"}`)}},
			{Text: "done"},
		}}
		a, j, sh, _, cwd := newAgent(t, prov)
		a.Policy, a.Cfg.HooksDir = pol, ""
		ex := tools.Exec{Dir: cwd, Shell: "zsh", Opts: []string{"equals"}, GlobalAliases: tc.aliases}
		if err := a.Start(context.Background(), "go", ex); err != nil {
			t.Fatal(err)
		}
		if got := len(sh.handed) == 1; got != tc.handed {
			t.Errorf("aliases %q: handed off %q, journal %s", tc.aliases, sh.handed, kinds(j.es))
		}
	}
}
