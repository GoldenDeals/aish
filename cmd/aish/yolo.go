package main

import (
	"errors"
	"fmt"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// yoloCmd turns the checks of the assistant's calls off for the rest of
// this shell's life (`aish yolo`), or on again (`aish yolo off`). The proxy
// refuses the assistant and a process in the background, and asks the user
// before the checks go off.
func yoloCmd(args []string) int {
	on := true
	switch {
	case len(args) == 0, len(args) == 1 && args[0] == "on":
	case len(args) == 1 && args[0] == "off":
		on = false
	default:
		return fail(errors.New("usage: aish yolo [off]"))
	}
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	if !on {
		if err := client.Call(rpc.MethodYolo, rpc.YoloParams{}, nil); err != nil {
			return fail(err)
		}
		fmt.Println("yolo off: the assistant's calls are checked again")
		return 0
	}
	// Interrupted after the Yes, the proxy answers that it is on.
	if interrupted, err := confirmYolo(client.Path); err != nil {
		if interrupted {
			fail(errors.New("yolo: interrupted, the checks stay on"))
			return 130
		}
		return fail(err)
	}
	fmt.Println("yolo: no policies, [policy] rules, questions or limits of the subagents' bash for the assistant " +
		"till this shell exits (the guard and the hooks stay); aish yolo off turns the checks back on")
	return 0
}

// confirmYolo makes the call of aish yolo, which the proxy answers once the
// user answered its question (confirmCall).
func confirmYolo(path string) (interrupted bool, err error) {
	return confirmCall(path, rpc.MethodYolo, rpc.YoloParams{On: true}, nil)
}
