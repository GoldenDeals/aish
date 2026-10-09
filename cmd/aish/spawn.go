package main

import (
	"fmt"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// spawnCmd has the proxy start a subagent in the background on the user's
// text, as a line `&NAME text` at the prompt asks, and tells where its
// answer will be: the first prompt after it ended tells again.
func spawnCmd(params rpc.SpawnParams) int {
	var t rpc.Task
	if code := requestInto(rpc.MethodAgentSpawn, params, &t); code != 0 {
		return code
	}
	fmt.Printf("\x1b[2m[aish: %s started in the background (%s), see aish tasks show %s]\x1b[0m\n", t.Agent, t.ID, t.ID)
	return 0
}
