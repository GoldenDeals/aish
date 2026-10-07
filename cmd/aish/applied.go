package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// The config a command in the shell shows and goes by. Inside aish it is
// the one in force, which the requests go by: the proxy's (rpc config),
// config.toml as aish read it at its start or at the last `aish
// apply-config`, for the shell's profile, with the project file of the
// directory as the first request from there read it. An edit on disk not
// applied yet is only named: `aish policy` that answers by a rule the
// agent does not have yet, or `aish model` that takes a profile the proxy
// does not know, would mislead. Outside aish it is the files on disk, as
// a new aish would take them.

// inForceCmds go by the config in force inside aish, the sessions by the
// proxy's directory of them (rpc.Info.Dir); configless need no config of
// their own, the agent and the session being the proxy's, the subagents'
// files found from cwd.
var (
	inForceCmds = map[string]bool{"context": true, "hooks": true, "mcp": true, "model": true, "policy": true,
		"resume": true, "session": true, "skills": true, "status": true, "tool": true}
	configless = map[string]bool{"agent": true, "agents": true, "clear": true, "compact": true, "expand": true, "new": true, "tasks": true}
)

// byProxy tells whether, inside aish, the command of args goes by the
// proxy, not by config.toml on disk: run does not stop it at a file broken
// there, which `aish apply-config` would tell about and refuse. Requests
// go on with the config in force. Outside aish nothing is in force but the
// file.
func byProxy(args []string) bool {
	return len(args) > 0 && os.Getenv("AISH_SOCK") != "" && (inForceCmds[args[0]] || configless[args[0]])
}

// applied is the config of a command in a directory.
type applied struct {
	// cfg is of the profile asked for, the project file laid over; the
	// model and the effort are config.toml's, the shell's are rpc.Info's.
	// From the proxy it has no API keys.
	cfg     config.Config
	def     string // the profile config.toml selects for the shell
	project string // the project file of the directory, "" if none
	global  int    // how many [policy] rules of cfg are config.toml's
	// policies are the files of the Cedar policies in force, when asked
	// for; policyErr says why there are none.
	policies  []policy.Summary
	policyErr error
	changed   []string // the config files on disk that differ from those in force
	// profErr: a proxy older than rpc config, whose shell's profile the
	// files on disk have not.
	profErr error
}

// inForce is the config of a command in cwd: inside aish the proxy's, of
// the shell's profile or, if given, of profile; outside, disk, what run
// read of config.toml, with the project file of cwd. With policies, the
// policies in force too.
func inForce(disk config.Config, cwd string, profile *string, policies bool) (applied, error) {
	client, err := rpc.FromEnv()
	if err != nil {
		return onDisk(disk, cwd, profile, policies)
	}
	var res rpc.Config
	err = client.Call(rpc.MethodConfig, rpc.ConfigParams{Cwd: cwd, Env: os.Environ(), Profile: profile, Policies: policies}, &res)
	switch {
	case olderProxy(err):
		// As before rpc config: the files, the shell's profile.
		shell, perr := disk, error(nil)
		var info rpc.Info
		if profile == nil && client.Call(rpc.MethodInfo, nil, &info) == nil {
			shell, perr = profileOf(disk, info)
		}
		a, err := onDisk(shell, cwd, profile, policies)
		a.def, a.profErr = disk.Profile, perr
		return a, err
	case err != nil:
		return applied{}, err
	}
	if res.DefaultErr != "" && profile == nil {
		fmt.Fprintf(os.Stderr, "\x1b[33maish: %s; on the top level of config.toml\x1b[0m\n", res.DefaultErr)
	}
	a := applied{cfg: res.Config, def: res.Default, project: res.Project, global: res.Global,
		policies: res.Policies, changed: res.Changed}
	if res.PolicyErr != "" {
		a.policyErr = errors.New(res.PolicyErr)
	}
	return a, nil
}

// onDisk is inForce from the files on disk, cfg what run read of
// config.toml.
func onDisk(cfg config.Config, cwd string, profile *string, policies bool) (applied, error) {
	a := applied{def: cfg.Profile}
	if profile != nil && *profile != cfg.Profile {
		var err error
		if cfg, err = config.LoadProfile(*profile); err != nil {
			return applied{}, err
		}
	}
	a.global = rulesOf(cfg).Len()
	cfg, project, err := config.Project(cfg, cwd)
	if err != nil {
		return applied{}, err
	}
	a.cfg, a.project = cfg, project
	if policies {
		var eng *policy.Engine
		if eng, a.policyErr = policy.Load(context.Background(), cfg.PolicyDir, rulesOf(cfg)); a.policyErr == nil {
			a.policies = eng.Summary()
		}
	}
	return a, nil
}

// modelsWait is how long `aish model` waits for the list of the models:
// the proxy gives the API 15 seconds.
const modelsWait = 20 * time.Second

// listModels has the proxy list the models of profile of the config in
// force: the key is there, not in the config a command gets. A proxy older
// than rpc models leaves it to prov, then made of the files on disk.
func listModels(client *rpc.Client, profile string, prov llm.Provider) ([]llm.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsWait)
	defer cancel()
	var ms []llm.ModelInfo
	err := client.CallContext(ctx, rpc.MethodModels, rpc.ModelsParams{Profile: profile, Env: os.Environ()}, &ms)
	if olderProxy(err) {
		return prov.Models(ctx)
	}
	return ms, err
}

// olderProxy tells whether err is of a proxy started by an aish older than
// the method called.
func olderProxy(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "unknown method ")
}

// changedNote is the line, with its newline, that names the config files
// on disk not in force yet, "" if there are none: what the command shows
// is the config in force.
func changedNote(changed []string) string {
	if len(changed) == 0 {
		return ""
	}
	names := make([]string, len(changed))
	for i, f := range changed {
		names[i] = home(f)
	}
	return fmt.Sprintf("\x1b[33mconfig changed on disk: aish apply-config to apply %s\x1b[0m\n", strings.Join(names, ", "))
}

// notInForce is why `aish model WORD` does not take WORD for a profile
// when it is one of config.toml on disk only: the proxy goes by the config
// in force, and WORD would be taken for a model. nil when WORD is not that.
func notInForce(cfg config.Config, word string) error {
	if _, ok := cfg.Profiles[word]; ok || word == config.Root {
		return nil
	}
	top, err := config.LoadProfile("")
	if _, ok := top.Profiles[word]; err != nil || !ok {
		return nil
	}
	return fmt.Errorf("no profile %q in the config in force; aish apply-config applies config.toml as it is now", word)
}
