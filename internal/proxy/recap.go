package proxy

import (
	"context"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// recap has the agent retell the whole session on the screen. It is
// refused as compact is: from the agent's command (errNested), during a
// request (errBusy), from a process not in the shell's foreground
// (errNotShell). It changes nothing, but it takes the request's turn and
// a call to the model the user pays for.
func (p *Proxy) recap(ctx context.Context, ap rpc.AgentParams) error {
	fg := p.fromShell(ctx)
	p.mu.Lock()
	err := p.nested(fg)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return p.request(ctx, execOf(ap), true, func(ctx context.Context, a *agent.Agent) error {
		return a.Recap(ctx)
	})
}
