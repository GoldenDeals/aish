package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/session"
)

const maxInstructionBytes = 40000

// Instruction files, as in Claude Code: global ones, then every directory
// from / down to the working directory.
var dirInstructions = []struct{ name, kind string }{
	{"CLAUDE.md", "project instructions, checked into the codebase"},
	{".claude/CLAUDE.md", "project instructions, checked into the codebase"},
	{"AGENTS.md", "project instructions, checked into the codebase"},
	{"CLAUDE.local.md", "user's private project instructions, not checked in"},
}

// instructionFile is one instruction file to put in front of the model.
type instructionFile struct{ path, kind string }

// instructionFiles lists the instruction files that apply in cwd, most
// general first.
func instructionFiles(cwd string) []instructionFile {
	home, _ := os.UserHomeDir()
	var out []instructionFile
	seen := map[string]bool{}
	add := func(path, kind string) {
		if abs, err := filepath.EvalSymlinks(path); err == nil && !seen[abs] {
			if st, err := os.Stat(abs); err == nil && st.Mode().IsRegular() {
				seen[abs] = true
				out = append(out, instructionFile{path, kind})
			}
		}
	}
	for _, p := range []string{filepath.Join(home, ".claude", "CLAUDE.md"), filepath.Join(config.Dir(), "CLAUDE.md")} {
		add(p, "user's private global instructions for all projects")
	}
	var dirs []string
	for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		for _, f := range dirInstructions {
			add(filepath.Join(dirs[i], f.name), f.kind)
		}
	}
	return out
}

// instructions returns entries for the instruction files of cwd that the
// session has not seen yet, or has seen with different content. They are
// read when the user asks something from a directory, not when they cd.
func instructions(entries []session.Entry, cwd string) []session.Entry {
	known := map[string]string{}
	for _, e := range entries {
		if e.Kind == session.KindInstructions {
			known[e.Path] = e.Text
		}
	}
	var out []session.Entry
	for _, f := range instructionFiles(cwd) {
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(b))
		if len(text) > maxInstructionBytes {
			text = text[:maxInstructionBytes] + "\n[truncated]"
		}
		if prev, ok := known[f.path]; text == "" || ok && prev == text {
			continue
		}
		out = append(out, session.Entry{Kind: session.KindInstructions, Path: f.path, Text: text, About: f.kind})
	}
	return out
}

func instructionsBlock(es []session.Entry) string {
	var b strings.Builder
	b.WriteString("<system-reminder>\nCodebase and user instructions are shown below. Be sure to adhere to these instructions. " +
		"IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written. " +
		"Instructions in a directory apply to work in that directory and below; more specific ones win.\n")
	for _, e := range es {
		b.WriteString("\nContents of " + e.Path + " (" + e.About + "):\n\n" + e.Text + "\n")
	}
	b.WriteString("</system-reminder>")
	return b.String()
}
