package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// clearCmd starts a new session; the one left stays on disk, as every
// session with entries does, for aish resume.
func clearCmd(args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish clear (aish new NAME starts a session with a name)"))
	}
	return startOver(rpc.ClearParams{})
}

// newCmd starts a new session named as the rest of the arguments say:
// `aish new log triage` needs no quotes.
func newCmd(args []string) int {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return fail(errors.New("usage: aish new [NAME]"))
	}
	return startOver(rpc.ClearParams{Name: strings.Join(args, " ")})
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
	// The proxy refuses it anyway, in these words; refused here, as aish
	// model and aish resume are, it is before the rpc meant to change the
	// session.
	if old.Asking {
		return fail(errors.New("sessions are cleared by the user, not by the assistant"))
	}
	if err := client.Call(rpc.MethodClear, cp, &info); err != nil {
		return fail(err)
	}
	fmt.Println(startedOver(cp, old, info))
	return 0
}

// startedOver is the line startOver prints: old is the session left, info
// the new one. One left without entries is not on disk: there was nothing
// to keep.
func startedOver(cp rpc.ClearParams, old, info rpc.Info) string {
	next := "new session " + info.SessionID
	if cp.Name != "" {
		next = fmt.Sprintf("new session %s (%s)", cp.Name, info.SessionID)
	}
	if !old.Saved {
		return next
	}
	left := old.SessionID
	if old.Name != "" {
		left = fmt.Sprintf("%s (%s)", old.Name, old.SessionID)
	}
	return fmt.Sprintf("left %s, %s", left, next)
}
