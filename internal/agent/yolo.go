package agent

import (
	"context"

	"github.com/GoldenDeals/aish/internal/policy"
)

// With aish yolo the user has the checks of the agent's calls off till the
// shell exits: the Cedar policies and the [policy] rules give way to the
// guard alone, which keeps the agent from trusting a project and from
// turning yolo on by the text of its line (code it leaves the shell past
// that text meets the proxy's question, internal/proxy/yolo.go); a verdict
// of ask, of a hook too, is allow without a question; a subagent's bash
// goes past the scope of its file. The pre-tool hooks, the user's code and not
// aish's checks, run as ever: a deny of theirs stays. The host has the
// switch (Agent.Yolo) and the agent asks it at each call: a subagent in the
// background, started before the switch, follows it. The system prompt
// does not change: the hints of the policies stay, and the cache with them.

// yolo tells whether the user has the checks off now.
func (a *Agent) yolo() bool {
	return a.Yolo != nil && a.Yolo()
}

// check is the verdict on a call, made as in: the policy's, or under yolo
// the guard's.
func (a *Agent) check(ctx context.Context, in policy.Input) (policy.Decision, error) {
	if a.yolo() {
		return a.Policy.Guard(ctx, in)
	}
	return a.Policy.Check(ctx, in)
}
