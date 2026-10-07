package proxy

import (
	"encoding/json"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// tasks answers `aish tasks`: the agent's subagents in the background, or
// the output of one. It only reads them, so it is not the user's alone:
// the assistant's command sees what the assistant may ask task_result for,
// and its output reaches the model masked, as any command's. Nor does it
// wait for the request in progress (reqMu): the agent keeps the set under
// locks of its own, which the subagents' goroutines take too.
func (p *Proxy) tasks(params json.RawMessage) (any, error) {
	var tp rpc.TasksParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &tp); err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	a := p.ag
	p.mu.Unlock()
	if a == nil {
		a = &agent.Agent{} // no request yet, nor a subagent
	}
	if tp.ID == "" {
		return append([]rpc.Task{}, a.BackgroundTasks()...), nil
	}
	return a.BackgroundTask(tp.ID)
}
