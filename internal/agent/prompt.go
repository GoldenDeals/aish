package agent

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// The system prompt's own text is built in, a file a section:
// prompt/NAME.md. It began as Claude Code's system prompt (v2.1.286, as
// published in github.com/Piebald-AI/claude-code-system-prompts) and was
// rewritten for aish: its tools, its policy, the live shell (research
// 236-1N). It says nothing that changes from turn to turn, nor between
// modes the user switches during a session: the provider caches the system
// prompt ahead of the whole conversation, and a change there writes all of
// it to the cache anew.

// promptSection is one section of the system prompt's own text.
type promptSection struct {
	name string
	// overridable marks a section on how to work (what to do, what to
	// say), which the user may want otherwise, rather than one on how aish
	// works, which the shell depends on.
	overridable bool
}

// hostSections are the sections of the host agent's system prompt, in
// order.
var hostSections = []promptSection{
	{name: "intro"},
	{name: "harness"},
	{name: "shell"},
	{name: "tools"},
	{name: "tasks", overridable: true},
	{name: "autonomy", overridable: true},
	{name: "care", overridable: true},
	{name: "git", overridable: true},
	{name: "reporting", overridable: true},
	{name: "output", overridable: true},
}

//go:embed prompt/*.md
var promptFiles embed.FS

// sectionText is the text of each section, by name.
var sectionText = loadSections(promptFiles, hostSections)

// loadSections reads the sections of lists from prompt/ in fsys, each
// without its file's trailing newlines: the prompt's bytes do not depend on
// the editor. A file no list names, a section without a file or one named
// twice in a list is a mistake of the build: it panics as the package
// loads, and any test of the package finds it.
func loadSections(fsys fs.FS, lists ...[]promptSection) map[string]string {
	text := map[string]string{}
	for _, list := range lists {
		seen := map[string]bool{}
		for _, s := range list {
			if seen[s.name] {
				panic(fmt.Sprintf("system prompt: section %s is listed twice", s.name))
			}
			seen[s.name] = true
			b, err := fs.ReadFile(fsys, "prompt/"+s.name+".md")
			if err != nil {
				panic(fmt.Sprintf("system prompt: section %s: %v", s.name, err))
			}
			text[s.name] = strings.TrimRight(string(b), "\n")
		}
	}
	files, err := fs.Glob(fsys, "prompt/*.md")
	if err != nil {
		panic(fmt.Sprintf("system prompt: %v", err))
	}
	for _, f := range files {
		if _, ok := text[strings.TrimSuffix(path.Base(f), ".md")]; !ok {
			panic(fmt.Sprintf("system prompt: %s is no listed section", f))
		}
	}
	return text
}

// sections is the text of list, its sections in order.
func sections(list []promptSection) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = sectionText[s.name]
	}
	return strings.Join(parts, "\n\n")
}
