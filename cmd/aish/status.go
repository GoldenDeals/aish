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

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/mcp"
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
	row("journal", filepath.Join(dir, info.SessionID+".jsonl"))

	head("model")
	row("provider", cfg.Provider)
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
	sc := cfg
	sc.Effort = info.Effort
	maxTokens := fmt.Sprint(sc.ReplyTokens())
	if cfg.MaxTokens == 0 {
		maxTokens += " (by the effort; max_tokens sets it)"
	}
	row("max_tokens", maxTokens)

	head("settings")
	row("max_steps", fmt.Sprint(cfg.MaxSteps))
	row("max_output", fmt.Sprintf("%d bytes", cfg.MaxOutputBytes))
	row("fold_lines", fmt.Sprint(cfg.FoldLines))
	row("markdown", fmt.Sprint(cfg.Markdown))
	row("prompt_status", fmt.Sprint(cfg.PromptStatus))
	row("config", configPath())
	rego, _ := filepath.Glob(filepath.Join(cfg.PolicyDir, "*.rego"))
	row("policy", fmt.Sprintf("%s (%d files)", cfg.PolicyDir, len(rego)))
	row("tools", cfg.ToolsDir)
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

// modelArgs is what `aish model [NAME] [EFFORT]` switches to; effort ""
// is the model's default.
type modelArgs struct {
	name, effort       string
	setName, setEffort bool
}

// parseModelArgs reads aish model [NAME] [EFFORT|default], where a lone
// EFFORT keeps the model.
func parseModelArgs(provider string, args []string) (modelArgs, error) {
	levels := llm.Efforts(provider)
	isEffort := func(s string) bool { return s == "default" || slices.Contains(levels, s) }
	var m modelArgs
	switch {
	case len(args) > 2:
		return m, errors.New("usage: aish model [NAME] [EFFORT|default]")
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

// modelCmd lists the models or switches this shell's model and effort.
func modelCmd(cfg config.Config, args []string) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	want, err := parseModelArgs(cfg.Provider, args)
	if err != nil {
		return fail(err)
	}
	levels := llm.Efforts(cfg.Provider)
	conf := cfg
	cfg = shellConfig(cfg, client)
	// The list is asked without the effort: a wrong one is what may need fixing.
	lc := cfg
	lc.Effort = ""
	prov, err := llm.New(lc)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ms, listErr := prov.Models(ctx)
	slices.SortFunc(ms, func(a, b llm.ModelInfo) int { return strings.Compare(a.ID, b.ID) })

	if len(args) == 0 {
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
		fmt.Print(" · aish model [NAME] [EFFORT|default]\x1b[0m\n")
		return 0
	}

	name, effort, setEffort := cfg.Model, cfg.Effort, want.setEffort
	if want.setName {
		name = want.name
	}
	if setEffort {
		effort = want.effort
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
	if err := client.Call(rpc.MethodModel, rpc.ModelParams{Model: name, Effort: effort, Window: window}, nil); err != nil {
		return fail(err)
	}
	var keep []string
	if name != conf.Model {
		keep = append(keep, "model")
	}
	if effort != conf.Effort {
		keep = append(keep, "effort")
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
