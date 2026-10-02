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
// running the model: `aish policy bash 'sudo ls'`. The [policy] rules of
// the config are one more checker of the engine and answer there too.
func policyCmd(cfg config.Config, args []string) int {
	global := rulesOf(cfg).Len()
	// The project's policies too, as the agent would have them here.
	cwd, _ := os.Getwd()
	cfg, project, err := config.Project(cfg, cwd)
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	rules := rulesOf(cfg)
	eng, err := policy.Load(ctx, cfg.PolicyDir, rules)
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
		line := "no policies in " + cfg.PolicyDir
		if n > 0 {
			line = fmt.Sprintf("%d policies in %s: %s", n, cfg.PolicyDir, strings.Join(files, ", "))
		}
		for _, r := range []struct {
			n    int
			from string
		}{{global, "config.toml"}, {rules.Len() - global, project}} {
			switch {
			case r.n == 1:
				line += " + 1 rule from " + r.from
			case r.n > 1:
				line += fmt.Sprintf(" + %d rules from %s", r.n, r.from)
			}
		}
		fmt.Println(line)
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

// rulesOf is the [policy] table of cfg as the policy package takes it.
func rulesOf(cfg config.Config) policy.Rules {
	return policy.Rules{Deny: cfg.Policy.Deny, Ask: cfg.Policy.Ask, WriteOutsideHome: cfg.Policy.WriteOutsideHome}
}
