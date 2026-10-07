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
	"github.com/inebotov/aish/internal/skills"
	"github.com/inebotov/aish/internal/tools"
)

// skillsCmd lists the skills that apply in cwd and what is wrong with them.
func skillsCmd(cfg config.Config, args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish skills"))
	}
	cwd, _ := os.Getwd()
	found, problems := skills.Find(cwd)
	reg := tools.Load(cfg.ToolsDir)
	var shown []skills.Skill
	nameW, srcW := 0, 0
	for _, s := range found {
		if _, taken := reg.Get(s.Name); taken {
			problems = append(problems, skills.Problem{Path: s.File(), Msg: "skipped, a tool has this name"})
			continue
		}
		if p := shadowed(s.Name); p != "" {
			problems = append(problems, skills.Problem{Path: s.File(), Msg: "typed as a command it runs " + p + "; use /" + s.Name})
		}
		shown = append(shown, s)
		nameW, srcW = max(nameW, runewidth.StringWidth(call(s))), max(srcW, len(source(s, cwd)))
	}
	if len(shown) == 0 && len(problems) == 0 {
		fmt.Println("no skills in ~/.claude/skills, ~/.config/aish/skills or .claude/skills")
		return 0
	}
	width := 0
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		width = w
	}
	for _, s := range shown {
		head := runewidth.FillRight(call(s), nameW) + "  " + fmt.Sprintf("%-*s  ", srcW, source(s, cwd))
		desc := strings.Join(strings.Fields(s.Desc), " ")
		if s.UserOnly {
			desc = "[not for the model] " + desc
		}
		if n := width - 1 - runewidth.StringWidth(head); n > 0 {
			desc = runewidth.Truncate(desc, n, "…")
		}
		fmt.Printf("\x1b[1m%s\x1b[0m%s%s\n", s.Name, head[len(s.Name):], desc)
	}
	for _, p := range problems {
		fmt.Printf("\x1b[33mproblem:\x1b[0m %s: %s\n", home(p.Path), p.Msg)
	}
	return 0
}

// call is how the skill is typed: its name and argument-hint.
func call(s skills.Skill) string {
	return strings.TrimSpace(s.Name + " " + s.Hint)
}

// source is where a skill comes from: ~/.claude, ./.claude, ../.claude.
func source(s skills.Skill, cwd string) string {
	if !s.Project {
		return home(s.Root)
	}
	rel, err := filepath.Rel(cwd, s.Root)
	switch {
	case err != nil:
		return home(s.Root)
	case strings.HasPrefix(rel, ".."):
		return rel
	}
	return "./" + rel
}

// shadowed is the command on PATH that bash runs for name, so a skill typed
// as a command does not reach the assistant.
func shadowed(name string) string {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
