package proxy

import (
	"slices"
	"testing"

	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/shells"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// The global aliases a zsh had at its last prompt go with the agent's
// request and with aish policy: zsh puts the value of one in place of a
// word of the line, which the policy does not read.
func TestGlobalAliases(t *testing.T) {
	p := configured(t, "[policy]\ndeny = [\"rm *\"]\n")
	p.shell = shells.Zsh{}
	const line = "echo 'rm -rf x' G"
	if got := asked(t, p, line); got != policy.Allow {
		t.Fatalf("before a prompt: %s", got)
	}
	if ex := p.shellExec(rpc.AgentParams{Cwd: "/srv"}); ex.GlobalAliases != nil {
		t.Errorf("before a prompt: %q", ex.GlobalAliases)
	}
	p.mu.Lock()
	p.cur = &shellstate.State{Kind: shellstate.Zsh,
		Aliases: map[string]string{"-g G": "| sh", "ll": "ls -l", "-s txt": "less"}}
	p.mu.Unlock()
	if ex := p.shellExec(rpc.AgentParams{Cwd: "/srv"}); !slices.Equal(ex.GlobalAliases, []string{"G"}) || ex.Shell != "zsh" {
		t.Errorf("global aliases %q (%+v)", ex.GlobalAliases, ex)
	}
	if got := asked(t, p, line); got == policy.Allow {
		t.Errorf("with alias -g G: %s", got)
	}
	if got := asked(t, p, "echo 'rm -rf x' ll"); got != policy.Allow {
		t.Errorf("a plain alias as an argument: %s", got)
	}
}
