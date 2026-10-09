package main

import (
	"errors"
	"fmt"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// yoloCmd turns the checks of the assistant's calls off for the rest of
// this shell's life (`aish yolo`), or on again (`aish yolo off`). The proxy
// refuses the assistant and a process in the background.
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
	if err := client.Call(rpc.MethodYolo, rpc.YoloParams{On: on}, nil); err != nil {
		return fail(err)
	}
	if on {
		fmt.Println("yolo: no policies, [policy] rules, questions or limits of the subagents' bash for the assistant " +
			"till this shell exits (the guard of aish trust and the hooks stay); aish yolo off turns the checks back on")
	} else {
		fmt.Println("yolo off: the assistant's calls are checked again")
	}
	return 0
}
