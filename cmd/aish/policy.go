package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/tools"
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
	fmt.Fprint(os.Stderr, untrustedNote(cfg, project))
	ctx := context.Background()
	rules := rulesOf(cfg)
	eng, err := policy.Load(ctx, cfg.PolicyDir, rules)
	if err != nil {
		return fail(err)
	}
	if len(args) == 0 {
		fmt.Println(policyLine(eng, cfg.PolicyDir, global, rules.Len(), project))
		return 0
	}
	reg := loadTools(cfg, nil)
	t, ok := reg.Get(args[0])
	if !ok {
		return fail(fmt.Errorf("no tool %q", args[0]))
	}
	targs, err := tools.ParseCLI(t.Name(), t.Args(), args[1:], os.Stdin)
	if err != nil {
		return fail(err)
	}
	in := policy.NewInput(t.Name(), targs, cwd)
	if h, ok := t.(tools.HandsOff); ok {
		if line, ok := h.Command(targs); ok {
			in.HandOff(line)
		}
	}
	in.Server = tools.ServerOf(t)
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

// policyLine says what policies are in force: the Cedar files of dir with
// their policy counts and the [policy] rules, global from config.toml and
// the rest from the project's file. `aish policy` and `aish status` print it.
func policyLine(eng *policy.Engine, dir string, global, total int, project string) string {
	var list []string
	for _, d := range filepath.SplitList(dir) {
		list = append(list, home(d))
	}
	dirs := strings.Join(list, ", ")
	n := 0
	var files []string
	for _, s := range eng.Summary() {
		n += s.Policies
		files = append(files, fmt.Sprintf("%s (%d)", s.File, s.Policies))
	}
	line := "no policies in " + dirs
	switch {
	case n == 1:
		line = fmt.Sprintf("1 policy in %s: %s", dirs, strings.Join(files, ", "))
	case n > 1:
		line = fmt.Sprintf("%d policies in %s: %s", n, dirs, strings.Join(files, ", "))
	}
	for _, r := range []struct {
		n    int
		from string
	}{{global, "config.toml"}, {total - global, home(project)}} {
		switch {
		case r.n == 1:
			line += " + 1 rule from " + r.from
		case r.n > 1:
			line += fmt.Sprintf(" + %d rules from %s", r.n, r.from)
		}
	}
	return line
}

// rulesOf is the [policy] table of cfg as the policy package takes it.
func rulesOf(cfg config.Config) policy.Rules {
	return policy.Rules{Deny: cfg.Policy.Deny, Ask: cfg.Policy.Ask, WriteOutsideHome: cfg.Policy.WriteOutsideHome}
}
