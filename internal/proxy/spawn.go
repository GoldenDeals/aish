package proxy

import (
	"context"
	"fmt"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// The user starts a subagent in the background himself: a line `&NAME text`
// at the prompt has the shell run `aish agent spawn NAME -- text` (init.bash,
// init.zsh), and agentSpawn has the agent start it (agent.Spawn), readied
// as for a request but without a turn of the model and without a line in
// the journal. Its answer is the user's: `aish tasks show bgN`, and a line
// at the first prompt after it ended (tellEnded), which any task the user
// starts in the background gets.

// agentSpawn starts subagent sp.Agent on the user's text sp.Text. It is
// the user's as agent_start is, and refused as that is: to the agent's
// command, during a request, and to a process not in the foreground of the
// shell's terminal, such as the bash of a subagent in the background.
func (p *Proxy) agentSpawn(ctx context.Context, sp rpc.SpawnParams) (rpc.Task, error) {
	ap := sp.AgentParams
	fg := p.fromShell(ctx)
	p.mu.Lock()
	if err := p.nested(fg); err != nil {
		p.mu.Unlock()
		return rpc.Task{}, err
	}
	// The prompt's line is behind: as with ask-start, the output that
	// follows is no longer typed at it.
	p.dropLine()
	p.mu.Unlock()
	var t rpc.Task
	err := p.request(ctx, execOf(ap), true, func(_ context.Context, a *agent.Agent) error {
		var err error
		t, err = a.Spawn(sp.Agent, ap.Text, p.shellExec(ap))
		return err
	})
	return t, err
}

// tellEnded tells of the tasks the user started in the background that
// ended since the last prompt, a line each, before the status: at the
// prompt, not while he types, nor amid a command's output. Called under
// p.mu.
func (p *Proxy) tellEnded() {
	if p.ag == nil {
		return
	}
	ended := p.ag.Ended()
	if len(ended) == 0 {
		return
	}
	var b []byte
	if p.col.off {
		b = append(b, "\r\n"...) // the prompt would have followed the output there
	}
	for _, t := range ended {
		how := "finished"
		switch t.State {
		case "error":
			how = "failed"
		case "cancelled":
			how = "was cancelled"
		}
		b = fmt.Appendf(b, "%s[aish: %s %s in the background (%s), see aish tasks show %s]%s\r\n", dim, t.Agent, how, t.ID, t.ID, reset)
	}
	p.emit(b)
	p.col.off = false
}
