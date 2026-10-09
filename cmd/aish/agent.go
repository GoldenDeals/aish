package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// agentCmd carries a request to the agent, which lives in the proxy, and
// returns when the proxy has left a command for the shell or given the
// final answer.
func agentCmd(args []string) int {
	const agentUsage = "usage: aish agent start -- TEXT | aish agent resume ID RC"
	params := shellParams()
	var method string
	switch {
	case len(args) >= 1 && args[0] == "start":
		method, params.Text = rpc.MethodAgentStart, strings.Join(trimDashes(args[1:]), " ")
	case len(args) == 3 && args[0] == "resume":
		// Not 0 on garbage: the model would be told the command succeeded.
		rc, err := strconv.Atoi(args[2])
		if err != nil {
			return fail(fmt.Errorf("RC %q is not a number; %s", args[2], agentUsage))
		}
		method, params.ID, params.RC = rpc.MethodAgentResume, args[1], rc
	default:
		return fail(errors.New(agentUsage))
	}
	return request(method, params)
}

func compactCmd(args []string) int {
	params := shellParams()
	params.Text = strings.Join(args, " ")
	return request(rpc.MethodCompact, params)
}

// shellParams carry the shell's situation to the proxy, whose own is not
// the user's: tools run in this directory with this environment.
func shellParams() rpc.AgentParams {
	cwd, _ := os.Getwd()
	return rpc.AgentParams{Cwd: cwd, Env: os.Environ()}
}

// giveUpAfter is how long a stop asked for may take before the connection
// is closed instead, which cancels the request all the same.
const giveUpAfter = 5 * time.Second

// request makes the call and stays connected while the proxy works. Ctrl+C
// asks the proxy to stop and waits for the answer: the agent finishes what
// it prints before the shell goes on to its prompt. A second Ctrl+C gives
// up on the proxy.
func request(method string, params rpc.AgentParams) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.CallContext(ctx, method, params, nil) }()
	interrupted := false
	for {
		select {
		case err := <-done:
			switch {
			case interrupted:
				return 130
			case err != nil:
				return fail(err)
			}
			return 0
		case <-sig:
			if interrupted {
				cancel()
				continue
			}
			interrupted = true
			// Over a connection of its own: the request's carries nothing
			// after the request.
			go func() {
				if client.Call(rpc.MethodAgentCancel, nil, nil) != nil {
					cancel()
				}
			}()
			time.AfterFunc(giveUpAfter, cancel)
		}
	}
}
