package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
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
	// The settings of this directory, of the profile of the shell, which
	// `aish model` may have switched: what the next request would take.
	cwd, _ := os.Getwd()
	a, err := inForce(cfg, cwd, nil, true)
	if err != nil {
		return fail(err)
	}
	cfg, project, def, profErr := a.cfg, a.project, a.def, a.profErr
	// Past config.Project the project's rules are in the same list.
	global := a.global

	row := func(k, v string) { fmt.Printf("  \x1b[2m%-16s\x1b[0m %s\n", k, v) }
	head := func(s string) { fmt.Printf("\x1b[1m%s\x1b[0m\n", s) }

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
	row("hide_work", fmt.Sprint(cfg.HideWork))
	row("ignored", fmt.Sprintf("%d command patterns (journal_ignore), %d variable patterns (state_ignore)",
		len(cfg.JournalIgnore), len(cfg.StateIgnore)))
	row("markdown", fmt.Sprint(cfg.Markdown))
	row("prompt_status", fmt.Sprint(cfg.PromptStatus))
	row("config", configFiles(project, st.ProjectConfig, cfg.Untrusted))
	if note := changedNote(a.changed); note != "" {
		row("", strings.TrimSuffix(note, "\n"))
	}
	dirs := func(list string) string { return strings.Join(filepath.SplitList(list), ", ") }
	if info.Yolo {
		// Above the checks it has off; nothing while they are on.
		row("yolo", "\x1b[31mon: no policies, [policy] rules or questions for the assistant till this shell exits; aish yolo off\x1b[0m")
	}
	// As the agent has them: an error shows here, not on the next request.
	if a.policyErr != nil {
		// The row says policy already, and a validation error names its file.
		row("policy", "\x1b[31m"+strings.TrimPrefix(a.policyErr.Error(), "policy: ")+"\x1b[0m")
	} else {
		row("policy", policyLine(a.policies, cfg.PolicyDir, global, rulesOf(cfg).Len(), project))
	}
	row("tools", dirs(cfg.ToolsDir))
	row("hooks", hooksLine(cfg.HooksDir))
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
// for this directory, with the keys it is not trusted with, and the one
// the last request took when that is another (the shell has moved since).
func configFiles(project, last string, untrusted []string) string {
	s := configPath()
	if project != "" {
		s += " + " + home(project)
	}
	if len(untrusted) > 0 {
		s += fmt.Sprintf(" (untrusted: %s; aish trust)", strings.Join(untrusted, ", "))
	}
	if last != "" && last != project {
		s += fmt.Sprintf(" (the last request took %s)", home(last))
	}
	return s
}

// compactAt says when the session is compacted on its own: the share of
// the window and the tokens it comes to, or why it never is.
func compactAt(share float64, window int) string {
	pct := int(share*100 + 0.5)
	switch {
	case share == 0:
		return "off (compact_at = 0)"
	case window == 0:
		return fmt.Sprintf("%d%% of the window; the window is unknown, so never", pct)
	}
	// As agent.CompactLimit counts it.
	return fmt.Sprintf("%d%% of the window, at %s tokens", pct, session.Short(int(share*float64(window))))
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
func modelCmd(disk config.Config, args []string) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	// A proxy that does not answer leaves config.toml's list to show; a
	// switch fails on its own.
	var info rpc.Info
	if client.Call(rpc.MethodInfo, nil, &info) != nil {
		info = rpc.Info{}
	}
	// The proxy refuses the switch anyway, but only after the API was asked
	// for the models and warnings that read as a switch were printed. The
	// list is the assistant's to see.
	if info.Asking && len(args) > 0 {
		return fail(errors.New("the model is switched by the user, not by the assistant"))
	}
	// The profiles of the config in force: the proxy knows no other.
	cwd, _ := os.Getwd()
	cur, err := inForce(disk, cwd, nil, false)
	if err != nil {
		return fail(err)
	}
	conf := cur.cfg
	// Another profile comes with its own model and effort, unless given;
	// the shell's comes with the shell's.
	profile, args, switching := profileArg(conf, args)
	if !switching && len(args) > 0 {
		if err := notInForce(conf, args[0]); err != nil {
			return fail(err)
		}
	}
	cfg := conf
	shell := !switching && info.Model != ""
	if switching {
		other, err := inForce(disk, cwd, &profile, false)
		if err != nil {
			return fail(err)
		}
		cfg = other.cfg
	} else if cur.profErr != nil {
		// cfg is what config.toml selects, still worth showing; the
		// shell's model is of the profile gone and does not go over it.
		fmt.Printf("\x1b[33mprofile %s is not in config.toml: %v\x1b[0m\n", info.Profile, cur.profErr)
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
	ms, listErr := listModels(client, cfg.Profile, prov)
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
	if cfg.Profile != cur.def {
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
	if cfg.Profile != "" || switching || cfg.Profile != cur.def || cfg.Profile != info.Profile {
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
