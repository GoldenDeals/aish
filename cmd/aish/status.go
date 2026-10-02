package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/mcp"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

func statusCmd(cfg config.Config) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	// Counted by the proxy: the journal need not come over.
	var st rpc.Status
	if err := client.Call(rpc.MethodStatus, nil, &st); err != nil {
		return fail(err)
	}
	info := st.Info
	// The profile of the shell, which `aish model` may have switched.
	def := cfg.Profile
	cfg, profErr := profileOf(cfg, info)
	// The settings of this directory: what the next request would take.
	cwd, _ := os.Getwd()
	// Past config.Project the project's rules are in the same list.
	global := rulesOf(cfg).Len()
	cfg, project, err := config.Project(cfg, cwd)
	if err != nil {
		return fail(err)
	}

	row := func(k, v string) { fmt.Printf("  \x1b[2m%-16s\x1b[0m %s\n", k, v) }
	head := func(s string) { fmt.Printf("\x1b[1m%s\x1b[0m\n", s) }

	head("context")
	ctx := session.Short(st.Tokens) + " tokens"
	if info.Window > 0 {
		ctx = fmt.Sprintf("%s / %s (%d%%)", session.Short(st.Tokens), session.Short(info.Window), st.Tokens*100/info.Window)
	}
	if !st.Measured && st.Tokens > 0 {
		ctx += ", estimated"
	}
	row("used", ctx)
	row("entries", fmt.Sprintf("%d commands, %d requests since the last compact", st.Commands, st.Requests))
	row("session", fmt.Sprintf("%d tool calls, %d compacts, %s in (%s cached) / %s out tokens spent",
		st.ToolCalls, st.Compacts, session.Short(st.InputTokens), session.Short(st.CachedTokens), session.Short(st.OutputTokens)))
	dir := info.Dir
	if dir == "" { // a proxy started by an older aish
		dir = cfg.SessionsDir
	}
	journal := filepath.Join(dir, info.SessionID+".jsonl")
	if !info.Saved {
		journal = "not saved (aish clear save, aish new)"
	}
	row("journal", journal)

	head("model")
	if len(cfg.Profiles) > 0 || info.Profile != "" {
		prof := profileName(info.Profile)
		if info.Model != "" && info.Profile != def {
			prof += fmt.Sprintf(" (switched in this shell; config: %s)", profileName(def))
		}
		if profErr != nil {
			prof += ": " + profErr.Error()
		}
		row("profile", prof)
	}
	// Without the effort: it can only be wrong for the provider, and is
	// shown below.
	pc := cfg
	pc.Effort = ""
	prov, err := llm.New(pc)
	if err != nil {
		row("provider", err.Error())
	} else {
		row("provider", prov.Name())
	}
	base := cfg.BaseURL
	if base == "" {
		base = "the provider's API"
	}
	row("base_url", base)
	m := info.Model
	if m != cfg.Model {
		m += fmt.Sprintf(" (switched in this shell; config: %s)", cfg.Model)
	}
	row("model", m)
	window := "unknown"
	switch {
	case cfg.ContextWindow > 0:
		window = session.Short(cfg.ContextWindow) + " (context_window)"
	case info.Window > 0:
		window = session.Short(info.Window) + " (from the API)"
	}
	row("window", window)
	effort := effortName(info.Effort)
	if info.Effort == "" {
		effort = "the model's default"
	}
	if info.Model != "" && info.Effort != cfg.Effort {
		effort += fmt.Sprintf(" (switched in this shell; config: %s)", effortName(cfg.Effort))
	}
	row("effort", effort)
	maxTokens := "unknown"
	if prov != nil {
		maxTokens = fmt.Sprint(prov.MaxTokens(info.Effort))
		if cfg.MaxTokens == 0 {
			maxTokens += " (by the effort; max_tokens sets it)"
		}
	}
	row("max_tokens", maxTokens)

	head("settings")
	row("max_steps", fmt.Sprint(cfg.MaxSteps))
	row("max_output", fmt.Sprintf("%d bytes", cfg.MaxOutputBytes))
	mask, _ := agent.NewMasker(cfg.MaskDefaults, cfg.Mask)
	row("mask", fmt.Sprintf("%d patterns (%d built-in, %d from mask)", mask.Len(), mask.Len()-len(cfg.Mask), len(cfg.Mask)))
	row("fold_lines", fmt.Sprint(cfg.FoldLines))
	row("ignored", fmt.Sprintf("%d command patterns (journal_ignore), %d variable patterns (state_ignore)",
		len(cfg.JournalIgnore), len(cfg.StateIgnore)))
	row("markdown", fmt.Sprint(cfg.Markdown))
	row("prompt_status", fmt.Sprint(cfg.PromptStatus))
	row("config", configFiles(project, st.ProjectConfig))
	dirs := func(list string) string { return strings.Join(filepath.SplitList(list), ", ") }
	// Loaded as the agent would: an error shows here, not on the next request.
	if eng, err := policy.Load(context.Background(), cfg.PolicyDir, rulesOf(cfg)); err != nil {
		row("policy", "\x1b[31m"+dirs(cfg.PolicyDir)+": "+err.Error()+"\x1b[0m")
	} else {
		row("policy", policyLine(eng, cfg.PolicyDir, global, rulesOf(cfg).Len(), project))
	}
	row("tools", dirs(cfg.ToolsDir))
	remote, _ := mcp.Remote(client, false)
	row("mcp", fmt.Sprintf("%s (%d tools)", cfg.MCPConfig, len(remote)))
	return 0
}

func configPath() string {
	if p := os.Getenv("AISH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(config.Dir(), "config.toml")
}

// configFiles names the files in force: config.toml and the project's
// for this directory, and the one the last request took when that is
// another (the shell has moved since).
func configFiles(project, last string) string {
	s := configPath()
	if project != "" {
		s += " + " + home(project)
	}
	if last != "" && last != project {
		s += fmt.Sprintf(" (the last request took %s)", home(last))
	}
	return s
}

// modelArgs is what `aish model [PROFILE] [NAME] [EFFORT]` switches to,
// past the profile; effort "" is the model's default.
type modelArgs struct {
	name, effort       string
	setName, setEffort bool
}

// profileArg cuts the profile off aish model [PROFILE] [NAME] [EFFORT]:
// the first word, if cfg has a profile of that name or it is config.Root,
// the top level, "". It goes before a model or an effort of the same
// name, which are then given after it.
func profileArg(cfg config.Config, args []string) (string, []string, bool) {
	if len(args) > 0 {
		if args[0] == config.Root {
			return "", args[1:], true
		}
		if _, ok := cfg.Profiles[args[0]]; ok {
			return args[0], args[1:], true
		}
	}
	return "", args, false
}

// profileOf is cfg for the profile the shell uses, by its info: as
// config.toml has it, the shell's model and effort not laid over. On an
// error, such as a profile config.toml has no more, cfg is kept.
func profileOf(cfg config.Config, info rpc.Info) (config.Config, error) {
	if info.Model == "" || info.Profile == cfg.Profile { // "": a proxy older than aish model
		return cfg, nil
	}
	pc, err := config.LoadProfile(info.Profile)
	if err != nil {
		return cfg, err
	}
	return pc, nil
}

func profileName(p string) string {
	if p == "" {
		return config.Root
	}
	return p
}

// parseModelArgs reads aish model [NAME] [EFFORT|default], where a lone
// EFFORT keeps the model; levels are those of the provider.
func parseModelArgs(provider string, levels []string, args []string) (modelArgs, error) {
	isEffort := func(s string) bool { return s == "default" || slices.Contains(levels, s) }
	var m modelArgs
	switch {
	case len(args) > 2:
		return m, errors.New("usage: aish model [PROFILE] [NAME] [EFFORT|default]")
	case len(args) == 2 && !isEffort(args[1]):
		return m, fmt.Errorf("no effort %q for %s (want %s or default)", args[1], provider, strings.Join(levels, ", "))
	case len(args) == 0:
		return m, nil
	}
	if e := args[len(args)-1]; isEffort(e) {
		m.effort, m.setEffort = e, true
		if e == "default" {
			m.effort = ""
		}
	}
	if len(args) == 2 || !m.setEffort {
		m.name, m.setName = args[0], true
	}
	return m, nil
}

// modelCmd lists the profiles and the models of this shell's, or switches
// its profile, model and effort.
func modelCmd(conf config.Config, args []string) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	// As in shellConfig, a proxy that does not answer leaves config.toml's
	// list to show; a switch fails on its own.
	var info rpc.Info
	if client.Call(rpc.MethodInfo, nil, &info) != nil {
		info = rpc.Info{}
	}
	// Another profile comes with its own model and effort, unless given;
	// the shell's comes with the shell's.
	profile, args, switching := profileArg(conf, args)
	var cfg config.Config
	shell := !switching && info.Model != ""
	if switching {
		if cfg, err = config.LoadProfile(profile); err != nil {
			return fail(err)
		}
	} else if cfg, err = profileOf(conf, info); err != nil {
		// cfg is conf, what config.toml has, still worth showing; the
		// shell's model is of the profile gone and does not go over it.
		fmt.Printf("\x1b[33mprofile %s is not in config.toml: %v\x1b[0m\n", info.Profile, err)
		shell = false
	}
	file := cfg // config.toml's, for how to keep the switch
	if shell {
		cfg.Model, cfg.Effort = info.Model, info.Effort
	}
	// The list is asked without the effort: a wrong one is what may need fixing.
	lc := cfg
	lc.Effort = ""
	prov, err := llm.New(lc)
	if err != nil {
		return fail(err)
	}
	levels := prov.Efforts()
	want, err := parseModelArgs(prov.Name(), levels, args)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ms, listErr := prov.Models(ctx)
	slices.SortFunc(ms, func(a, b llm.ModelInfo) int { return strings.Compare(a.ID, b.ID) })

	if len(args) == 0 && !switching {
		usage := "aish model [NAME] [EFFORT|default]"
		if len(conf.Profiles) > 0 {
			usage = "aish model [PROFILE] [NAME] [EFFORT|default]"
			names := append([]string{config.Root}, conf.ProfileNames()...)
			for i, n := range names {
				if n == profileName(cfg.Profile) {
					names[i] = "\x1b[0;1m*" + n + "\x1b[0;2m"
				}
			}
			fmt.Printf("\x1b[2mprofiles %s\x1b[0m\n", strings.Join(names, " "))
		}
		if listErr != nil {
			fmt.Printf("%s, effort %s\n", cfg.Model, effortName(cfg.Effort))
			return fail(fmt.Errorf("list models: %w", listErr))
		}
		known := false
		for _, m := range ms {
			mark, window := "  ", ""
			cur := m.ID == cfg.Model
			if cur {
				mark = "\x1b[1m* "
			}
			if m.Window > 0 {
				window = session.Short(m.Window)
			}
			fmt.Printf("%s%-40s\x1b[0m \x1b[2m%-6s%s\x1b[0m\n", mark, m.ID, window, effortLevels(m, cur, cfg.Effort))
			known = known || m.EffortsKnown
		}
		fmt.Printf("\x1b[2meffort %s", effortName(cfg.Effort))
		if !known {
			// A proxy such as cliproxyapi: the API refuses what does not fit.
			fmt.Printf(" · %s", strings.Join(levels, " "))
		}
		fmt.Printf(" · %s\x1b[0m\n", usage)
		return 0
	}

	name, effort, setEffort := cfg.Model, cfg.Effort, want.setEffort
	if want.setName {
		name = want.name
	}
	if setEffort {
		effort = want.effort
	}
	if e, dropped := fitEffort(prov, effort, setEffort); dropped {
		fmt.Printf("\x1b[33m%s takes no effort %s: back to its default\x1b[0m\n", prov.Name(), effort)
		effort = e
	}
	var model *llm.ModelInfo
	if listErr == nil {
		if i := slices.IndexFunc(ms, func(m llm.ModelInfo) bool { return m.ID == name }); i >= 0 {
			model = &ms[i]
		} else if name != cfg.Model {
			return fail(fmt.Errorf("no model %q; aish model lists them", name))
		}
	}
	window := 0
	if model != nil {
		window = model.Window
		if model.EffortsKnown && effort != "" && !slices.Contains(model.Efforts, effort) {
			switch {
			case !setEffort:
				fmt.Printf("\x1b[33m%s does not take effort %s: back to its default\x1b[0m\n", name, effort)
				effort = ""
			case len(model.Efforts) == 0:
				return fail(fmt.Errorf("%s takes no effort level", name))
			default:
				return fail(fmt.Errorf("%s takes effort %s", name, strings.Join(model.Efforts, ", ")))
			}
		}
	}
	mp := rpc.ModelParams{Profile: cfg.Profile, Model: name, Effort: effort, Window: window}
	if err := client.Call(rpc.MethodModel, mp, nil); err != nil {
		return fail(err)
	}
	var keep []string
	if cfg.Profile != conf.Profile {
		keep = append(keep, "profile")
	}
	if name != file.Model {
		keep = append(keep, "model")
	}
	if effort != file.Effort {
		keep = append(keep, "effort")
	}
	// The top level is named too when the shell comes to it, or config.toml
	// selects another.
	if cfg.Profile != "" || switching || cfg.Profile != conf.Profile || cfg.Profile != info.Profile {
		fmt.Printf("profile %s, ", profileName(cfg.Profile))
	}
	fmt.Printf("model %s, effort %s for this shell", name, effortName(effort))
	if len(keep) > 0 {
		fmt.Printf("; set %s in %s to keep it", strings.Join(keep, " and "), home(configPath()))
	}
	fmt.Println()
	return 0
}

func effortName(e string) string {
	if e == "" {
		return "default"
	}
	return e
}

// fitEffort is effort, or "" when it was not given and prov has no
// such level: the effort a profile does not set is the top level's,
// which may be of another provider. It tells whether it dropped one.
func fitEffort(prov llm.Provider, effort string, given bool) (string, bool) {
	if given || llm.CheckEffort(prov, effort) == nil {
		return effort, false
	}
	return "", true
}

// effortLevels shows what effort a model takes, the one in use bright.
func effortLevels(m llm.ModelInfo, cur bool, effort string) string {
	if !m.EffortsKnown {
		return ""
	}
	if len(m.Efforts) == 0 {
		return "no effort"
	}
	ls := slices.Clone(m.Efforts)
	for i, l := range ls {
		if cur && l == effort {
			ls[i] = "\x1b[0;1m" + l + "\x1b[0;2m"
		}
	}
	return "effort " + strings.Join(ls, " ")
}
