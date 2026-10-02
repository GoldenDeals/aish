package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inebotov/aish/internal/rpc"
)

// parseClear reads `aish clear [save [NAME]]`, NAME being the rest of the
// arguments: `aish clear save log triage` needs no quotes.
func parseClear(args []string) (rpc.ClearParams, error) {
	switch {
	case len(args) == 0:
		return rpc.ClearParams{}, nil
	case args[0] != "save" || len(args) > 1 && strings.HasPrefix(args[1], "-"):
		return rpc.ClearParams{}, errors.New("usage: aish clear [save [NAME]]")
	}
	return rpc.ClearParams{Save: true, Name: strings.Join(args[1:], " ")}, nil
}

// clearCmd starts a new session that is not saved, saving the current one
// first if asked.
func clearCmd(args []string) int {
	cp, err := parseClear(args)
	if err != nil {
		return fail(err)
	}
	return startOver(cp)
}

// newCmd starts a new session that is saved from its first entry.
func newCmd(args []string) int {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return fail(errors.New("usage: aish new [NAME]"))
	}
	return startOver(rpc.ClearParams{SaveNew: true, NewName: strings.Join(args, " ")})
}

// startOver has the proxy start this shell's session over and tells what
// became of the session left.
func startOver(cp rpc.ClearParams) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	var old, info rpc.Info
	if err := client.Call(rpc.MethodInfo, nil, &old); err != nil {
		return fail(err)
	}
	if err := client.Call(rpc.MethodClear, cp, &info); err != nil {
		return fail(err)
	}
	fmt.Println(startedOver(cp, old, info))
	return 0
}

// startedOver is the line startOver prints: old is the session left, info
// the new one.
func startedOver(cp rpc.ClearParams, old, info rpc.Info) string {
	switch {
	case cp.SaveNew && cp.NewName != "":
		return fmt.Sprintf("new session %s (%s, saved)", cp.NewName, info.SessionID)
	case cp.SaveNew:
		return fmt.Sprintf("new session %s (saved)", info.SessionID)
	case cp.Save && cp.Name != "":
		return fmt.Sprintf("saved %s as %s, new session %s", old.SessionID, cp.Name, info.SessionID)
	case cp.Save:
		return fmt.Sprintf("saved %s, new session %s", old.SessionID, info.SessionID)
	case old.Saved:
		return fmt.Sprintf("left %s, new session %s", old.SessionID, info.SessionID)
	}
	return fmt.Sprintf("dropped %s, new session %s", old.SessionID, info.SessionID)
}
