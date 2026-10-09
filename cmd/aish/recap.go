package main

import (
	"errors"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// recapCmd has the agent in the proxy retell the whole session, before
// summaries and clears too. The recap goes to the screen and nowhere else:
// the journal and the context stay as they were. Ctrl+C stops it, as it
// stops a request.
func recapCmd(args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish recap"))
	}
	return request(rpc.MethodRecap, shellParams())
}
