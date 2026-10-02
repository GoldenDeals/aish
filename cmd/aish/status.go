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
	var info rpc.Info
	if err := client.Call(rpc.MethodInfo, nil, &info); err != nil {
		return fail(err)
	}
	all, err := client.History()
	if err != nil {
		return fail(err)
	}
	es := session.Current(all)
	tokens := session.Tokens(es, cfg.MaxOutputBytes)

	var shell, asks, calls, summaries, in, out int
	measured := false
	for _, e := range all {
		switch e.Kind {
		case session.KindSummary:
			summaries++
		case session.KindAssistant:
			calls += len(e.ToolCalls)
			in += e.InputTokens
			out += e.OutputTokens
		}
	}
	for _, e := range es {
		switch e.Kind {
		case session.KindShell:
			shell++
		case session.KindUser:
			asks++
		case session.KindAssistant:
			measured = measured || e.InputTokens > 0
		}
	}

	row := func(k, v string) { fmt.Printf("  \x1b[2m%-16s\x1b[0m %s\n", k, v) }
	head := func(s string) { fmt.Printf("\x1b[1m%s\x1b[0m\n", s) }

	head("context")
	ctx := session.Short(tokens) + " tokens"
	if info.Window > 0 {
		ctx = fmt.Sprintf("%s / %s (%d%%)", session.Short(tokens), session.Short(info.Window), tokens*100/info.Window)
	}
	if !measured && tokens > 0 {
		ctx += ", estimated"
	}
	row("used", ctx)
	row("entries", fmt.Sprintf("%d commands, %d requests since the last compact", shell, asks))
	row("session", fmt.Sprintf("%d tool calls, %d compacts, %s in / %s out tokens spent",
		calls, summaries, session.Short(in), session.Short(out)))
	row("journal", filepath.Join(cfg.SessionsDir, info.SessionID+".jsonl"))

	head("model")
	row("provider", cfg.Provider)
	row("base_url", cfg.BaseURL)
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

// modelCmd lists the models or switches this shell's model and effort:
// aish model [NAME] [EFFORT], where a lone EFFORT keeps the model.
func modelCmd(cfg config.Config, args []string) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	levels := llm.Efforts(cfg.Provider)
	isEffort := func(s string) bool { return s == "default" || slices.Contains(levels, s) }
	switch {
	case len(args) > 2:
		return fail(errors.New("usage: aish model [NAME] [EFFORT|default]"))
	case len(args) == 2 && !isEffort(args[1]):
		return fail(fmt.Errorf("no effort %q for %s (want %s or default)", args[1], cfg.Provider, strings.Join(levels, ", ")))
	}
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

	name, effort, setEffort := cfg.Model, cfg.Effort, false
	if isEffort(args[len(args)-1]) {
		effort, setEffort = args[len(args)-1], true
		if effort == "default" {
			effort = ""
		}
	}
	if len(args) == 2 || !setEffort {
		name = args[0]
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
