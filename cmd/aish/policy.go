package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/shells"
	"github.com/GoldenDeals/aish/internal/tools"
)

// policyCmd shows the policies in force, and with a tool call asks them
// about it without running the model: `aish policy bash 'sudo ls'`; with
// --agent NAME, as a call of subagent NAME. The [policy] rules of the
// config are one more checker of the engine and answer there too. Inside
// aish they are the proxy's, which the requests go by: an edit is checked
// and put in force by `aish apply-config`. Outside, those on disk are
// loaded, so that a validation error shows up right after editing a file.
// --builtin prints the built-in policy, to copy into policy_dir.
func policyCmd(cfg config.Config, args []string) int {
	if len(args) > 0 && args[0] == "--builtin" {
		if len(args) > 1 {
			return fail(fmt.Errorf("usage: aish policy --builtin"))
		}
		fmt.Print(policy.BuiltinText)
		return 0
	}
	agent, args, err := agentFlag(args)
	if err != nil {
		return fail(err)
	}
	// The project's policies too, as the agent would have them here.
	cwd, _ := os.Getwd()
	a, err := inForce(cfg, cwd, nil, len(args) == 0)
	if err != nil {
		return fail(err)
	}
	cfg = a.cfg
	fmt.Fprint(os.Stderr, untrustedNote(cfg, a.project))
	fmt.Fprint(os.Stderr, changedNote(a.changed))
	if len(args) == 0 {
		if a.policyErr != nil {
			return fail(a.policyErr)
		}
		fmt.Println(policyLine(a.policies, cfg.PolicyDir, a.global, rulesOf(cfg).Len(), a.project))
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
	pp := rpc.PolicyParams{Cwd: cwd, Env: os.Environ(), Tool: t.Name(), Args: targs, Server: tools.ServerOf(t), Agent: agent}
	if h, ok := t.(tools.HandsOff); ok {
		pp.Line, pp.HandOff = h.Command(targs)
	}
	d, err := askPolicies(cfg, pp)
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

// askPolicies is what the policies in force say of the call pp: inside
// aish the proxy's, as compiled for its requests, made by the shell's
// model; outside, those of cfg, loaded now.
func askPolicies(cfg config.Config, pp rpc.PolicyParams) (policy.Decision, error) {
	model := cfg.Model
	if client, err := rpc.FromEnv(); err == nil {
		var d policy.Decision
		if err := client.Call(rpc.MethodPolicy, pp, &d); !olderProxy(err) {
			return d, err
		}
		// As before rpc policy: those on disk, the shell's model.
		var info rpc.Info
		if client.Call(rpc.MethodInfo, nil, &info) == nil && info.Model != "" {
			model = info.Model
		}
	}
	ctx := context.Background()
	eng, err := policy.Load(ctx, cfg.PolicyDir, rulesOf(cfg))
	if err != nil {
		return policy.Decision{}, err
	}
	// The shell config.toml starts, with its options not known.
	in := policy.NewInputIn(shells.Kind(cfg.Shell), pp.Tool, pp.Args, pp.Cwd, pp.Env)
	if pp.HandOff {
		in.HandOff(pp.Line)
	}
	in.Server, in.Model, in.Agent = pp.Server, model, pp.Agent
	return eng.Check(ctx, in)
}

// agentFlag takes a leading --agent NAME off the arguments of aish policy:
// the policies are asked about the call that follows as one of subagent
// NAME, which they see as context.agent. A flag after the tool's name is
// the tool's own.
func agentFlag(args []string) (string, []string, error) {
	usage := fmt.Errorf("usage: aish policy [--agent NAME] [TOOL ARGS...]")
	if len(args) == 0 {
		return "", args, nil
	}
	name, ok := strings.CutPrefix(args[0], "--agent=")
	switch {
	case ok:
		args = args[1:]
	case args[0] != "--agent":
		return "", args, nil
	case len(args) > 1:
		name, args = args[1], args[2:]
	default:
		return "", nil, usage
	}
	// Without a call there is nothing to ask as the subagent.
	if name == "" || len(args) == 0 {
		return "", nil, usage
	}
	return name, args, nil
}

// policyLine says what policies are in force: the built-in policy, the
// Cedar files of dir with their policy counts, as Engine.Summary has them,
// and the [policy] rules, global from config.toml and the rest from the
// project's file. `aish policy` and `aish status` print it.
func policyLine(summary []policy.Summary, dir string, global, total int, project string) string {
	var list []string
	for _, d := range filepath.SplitList(dir) {
		list = append(list, home(d))
	}
	dirs := strings.Join(list, ", ")
	n := 0
	var files []string
	builtin := ""
	for _, s := range summary {
		if s.Builtin {
			builtin = fmt.Sprintf("built-in (%d), ", s.Policies)
			continue
		}
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
	line = builtin + line
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
	return policy.Rules{Deny: cfg.Policy.Deny, Ask: cfg.Policy.Ask, WriteOutsideHome: cfg.Policy.WriteOutsideHome,
		Hints: cfg.Policy.Hints, WriteOutsideHomeHint: cfg.Policy.WriteOutsideHomeHint, Builtin: cfg.Policy.Builtin}
}
