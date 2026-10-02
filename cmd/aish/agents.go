package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/subagent"
)

// agentsCmd lists the subagents that apply in cwd and what is wrong with them.
func agentsCmd(_ config.Config, args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish agents"))
	}
	cwd, _ := os.Getwd()
	found, problems := subagent.Find(cwd)
	if len(found) == 0 && len(problems) == 0 {
		fmt.Println("no subagents in ~/.claude/agents, ~/.config/aish/agents or .claude/agents")
		return 0
	}
	nameW, srcW, modelW, toolsW := 0, 0, 0, 0
	for _, d := range found {
		nameW = max(nameW, runewidth.StringWidth(d.Name))
		srcW = max(srcW, runewidth.StringWidth(agentSource(d, cwd)))
		modelW = max(modelW, runewidth.StringWidth(agentModel(d)))
		toolsW = max(toolsW, runewidth.StringWidth(agentTools(d)))
	}
	width := 0
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		width = w
	}
	for _, d := range found {
		head := runewidth.FillRight(d.Name, nameW) + "  " +
			runewidth.FillRight(agentSource(d, cwd), srcW) + "  " +
			runewidth.FillRight(agentModel(d), modelW) + "  "
		if toolsW > 0 {
			head += runewidth.FillRight(agentTools(d), toolsW) + "  "
		}
		desc := strings.Join(strings.Fields(d.Desc), " ")
		if n := width - 1 - runewidth.StringWidth(head); n > 0 {
			desc = runewidth.Truncate(desc, n, "…")
		}
		fmt.Printf("\x1b[1m%s\x1b[0m%s%s\n", d.Name, head[len(d.Name):], desc)
	}
	for _, p := range problems {
		fmt.Printf("\x1b[33mproblem:\x1b[0m %s: %s\n", home(p.Path), p.Msg)
	}
	return 0
}

// agentModel is the model the subagent runs with; inherit is the host's.
func agentModel(d subagent.Def) string {
	if d.Model == "" {
		return "inherit"
	}
	return d.Model
}

// agentTools is the tools the subagent is limited to; empty when it is not.
func agentTools(d subagent.Def) string {
	if d.Tools == nil {
		return ""
	}
	return "tools: " + strings.Join(d.Tools, ", ")
}

// agentSource is where a subagent comes from: ~/.claude, ./.claude, ../.claude.
func agentSource(d subagent.Def, cwd string) string {
	if !d.Project {
		return home(d.Root)
	}
	rel, err := filepath.Rel(cwd, d.Root)
	switch {
	case err != nil:
		return home(d.Root)
	case strings.HasPrefix(rel, ".."):
		return rel
	}
	return "./" + rel
}
