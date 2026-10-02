package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
)

// policyCmd loads the policies, so that a validation error shows up right
// after editing a file, and with a tool call asks them about it without
// running the model: `aish policy bash 'sudo ls'`.
func policyCmd(cfg config.Config, args []string) int {
	// The project's policies too, as the agent would have them here.
	cwd, _ := os.Getwd()
	cfg, _, err := config.Project(cfg, cwd)
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	eng, err := policy.Load(ctx, cfg.PolicyDir)
	if err != nil {
		return fail(err)
	}
	if len(args) == 0 {
		n := 0
		var files []string
		for _, s := range eng.Summary() {
			n += s.Policies
			files = append(files, fmt.Sprintf("%s (%d)", s.File, s.Policies))
		}
		if n == 0 {
			fmt.Printf("no policies in %s\n", cfg.PolicyDir)
			return 0
		}
		fmt.Printf("%d policies in %s: %s\n", n, cfg.PolicyDir, strings.Join(files, ", "))
		return 0
	}
	reg := loadTools(cfg, nil)
	t, ok := reg.Get(args[0])
	if !ok {
		return fail(fmt.Errorf("no tool %q", args[0]))
	}
	targs, err := t.ParseCLI(args[1:], os.Stdin)
	if err != nil {
		return fail(err)
	}
	in := policy.NewInput(t.Name, targs, cwd)
	in.Server = t.Server
	in.Model = cfg.Model
	if client, err := rpc.FromEnv(); err == nil {
		in.Model = shellConfig(cfg, client).Model
	}
	d, err := eng.Check(ctx, in)
	if err != nil {
		return fail(err)
	}
	if d.Action == policy.Allow {
		fmt.Println(d.Action)
		return 0
	}
	fmt.Printf("%s: %s\n", d.Action, d.Reason)
	return 1
}
