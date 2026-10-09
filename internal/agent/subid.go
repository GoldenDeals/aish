package agent

import (
	"context"
	"strconv"
)

// A subagent's hooks are told which run of it an event is of (agent_id):
// the journals of all its runs are sub:NAME, and two runs of one subagent
// would be the same to them otherwise. A run task waits for is ID.N, the
// id of the call of task, as the host's journal has it, and the number of
// the task in the call, from 1. One in the background is its id there,
// bgN, as task_wait and aish tasks know it.

// callKey keys the id of the call a tool runs for in its context.
type callKey struct{}

func withCall(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callKey{}, id)
}

// callID is the id of the call ctx is of, "" when not known.
func callID(ctx context.Context) string {
	id, _ := ctx.Value(callKey{}).(string)
	return id
}

// nameRuns gives runs, the tasks of the call ctx is of in its order, the
// ids of runs task waits for.
func nameRuns(ctx context.Context, runs []*subRun) {
	call := callID(ctx)
	if call == "" {
		call = subName // a call made past the agent's call, as tests do
	}
	for i, r := range runs {
		r.id = call + "." + strconv.Itoa(i+1)
	}
}
