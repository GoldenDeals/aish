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
	row("max_tokens", fmt.Sprint(cfg.MaxTokens))

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

func modelCmd(cfg config.Config, args []string) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	if len(args) > 1 {
		return fail(errors.New("usage: aish model [NAME]"))
	}
	cfg = shellConfig(cfg, client)
	prov, err := llm.New(cfg)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ms, listErr := prov.Models(ctx)
	slices.SortFunc(ms, func(a, b llm.ModelInfo) int { return strings.Compare(a.ID, b.ID) })

	if len(args) == 0 {
		if listErr != nil {
			fmt.Println(cfg.Model)
			return fail(fmt.Errorf("list models: %w", listErr))
		}
		for _, m := range ms {
			mark, window := "  ", ""
			if m.ID == cfg.Model {
				mark = "\x1b[1m* "
			}
			if m.Window > 0 {
				window = session.Short(m.Window)
			}
			fmt.Printf("%s%-40s\x1b[0m \x1b[2m%s\x1b[0m\n", mark, m.ID, window)
		}
		return 0
	}

	name, window := args[0], 0
	if listErr == nil {
		i := slices.IndexFunc(ms, func(m llm.ModelInfo) bool { return m.ID == name })
		if i < 0 {
			return fail(fmt.Errorf("no model %q; aish model lists them", name))
		}
		window = ms[i].Window
	}
	if err := client.Call(rpc.MethodModel, rpc.ModelParams{Model: name, Window: window}, nil); err != nil {
		return fail(err)
	}
	fmt.Printf("model %s for this shell; set model in %s to keep it\n", name, configPath())
	return 0
}
