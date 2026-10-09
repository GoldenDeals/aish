// Package subagent reads Claude Code's subagent definitions: .md files in an
// agents directory, a frontmatter with the name, when to delegate to it, the
// tools it may and may not use, its permission mode and the model, then the
// subagent's system prompt. They are only files the agent builds a nested
// run from; this package runs nothing and caches nothing, as there are a
// few of them, read once per request.
package subagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/GoldenDeals/aish/internal/config"
)

// validName is what both APIs accept as a tool name, as for skills: the
// main agent is to name the subagent it delegates to.
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Def is one subagent as its file describes it.
type Def struct {
	Name string
	Desc string // description: when the main agent should delegate to it
	// Prompt is the body of the file: the subagent's system prompt.
	Prompt string
	// Tools are the names of the tools the subagent may use, as written in
	// the frontmatter; nil means all the tools of the host.
	Tools []string
	// Disallowed are the names of the tools it may not use, as written in
	// disallowedTools: an entry takes its tool away, whatever gives it.
	Disallowed []string
	// Mode is permissionMode as Claude Code spells it, "" without one.
	Mode string
	// Ignored are the fields of the frontmatter aish does not read, in the
	// order of the file.
	Ignored []string
	// Model is the model to run the subagent with; empty means the host's.
	Model string
	File  string // path of the .md file
	// Root is the directory its agents directory is in: ~/.claude,
	// ~/.config/aish or a project's .claude.
	Root string
	// Project subagents come from .claude of cwd or a directory above it.
	Project bool
}

// Problem is what is wrong with one file.
type Problem struct{ Path, Msg string }

// Find returns the subagents that apply in cwd, by name: the user's from
// ~/.claude/agents and ~/.config/aish/agents, then those from .claude/agents
// of every directory from / down to cwd. A nearer subagent replaces a
// farther one of the same name. With an empty cwd only the user's are
// looked for.
func Find(cwd string) ([]Def, []Problem) {
	home, _ := os.UserHomeDir()
	type root struct {
		dir     string
		project bool
	}
	roots := []root{{filepath.Join(home, ".claude"), false}, {config.Dir(), false}}
	if cwd != "" {
		var dirs []string
		for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
			dirs = append(dirs, d)
			if d == filepath.Dir(d) {
				break
			}
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			roots = append(roots, root{filepath.Join(dirs[i], ".claude"), true})
		}
	}
	byName := map[string]Def{}
	var problems []Problem
	// ~/.claude is also the .claude of $HOME on the way to cwd.
	seen := map[string]bool{}
	for _, r := range roots {
		dir := filepath.Join(r.dir, "agents")
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			file, ok := strings.CutSuffix(e.Name(), ".md")
			if !ok {
				continue
			}
			path := filepath.Join(dir, e.Name())
			// Stat, not the entry's type: a symlinked file counts.
			if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
				continue
			}
			d, err := load(path)
			// A file without a frontmatter, a README beside the
			// definitions, is not a subagent at all, not a broken one.
			if errors.Is(err, errNoFrontmatter) {
				continue
			}
			if err != nil {
				problems = append(problems, Problem{path, err.Error()})
				continue
			}
			if d.Name != file {
				problems = append(problems, Problem{path, fmt.Sprintf("name %q differs from the file name", d.Name)})
			}
			d.Root, d.Project = r.dir, r.project
			byName[d.Name] = d
		}
	}
	out := make([]Def, 0, len(byName))
	for _, d := range byName {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

func load(path string) (Def, error) {
	fm, body, err := parse(path)
	if fm.Name == "" {
		fm.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	switch {
	case err != nil:
		return Def{}, err
	case !validName.MatchString(fm.Name):
		return Def{}, fmt.Errorf("name %q: only letters, digits, - and _, up to 64", fm.Name)
	case strings.TrimSpace(fm.Description) == "":
		return Def{}, errors.New("no description in the frontmatter")
	case body == "":
		return Def{}, errors.New("no system prompt: the body after the frontmatter is empty")
	}
	mode, err := permissionMode(fm.PermissionMode)
	if err != nil {
		return Def{}, err
	}
	return Def{
		Name:       fm.Name,
		Desc:       strings.TrimSpace(fm.Description),
		Prompt:     body,
		Tools:      toolNames(fm.Tools),
		Disallowed: toolNames(fm.DisallowedTools),
		Mode:       mode,
		Ignored:    fm.ignored,
		Model:      model(fm.Model),
		File:       path,
	}, nil
}

// toolNames is the tools field, a string with commas or a list; nil when it
// names none. A comma within parentheses is a pattern's, as in
// Bash(git log, git diff): YAML splits a flow list there too, so the items
// of a list are put back together before the split.
func toolNames(v any) []string {
	var out []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	s := strings.Join(list(v), ",")
	depth, start := 0, 0
	for i, c := range s {
		switch {
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case c == ',' && depth == 0:
			add(s[start:i])
			start = i + 1
		}
	}
	add(s[start:])
	return out
}

// model is the model field as a model name for the provider. Claude Code's
// aliases mean the host's model: aish is not tied to one provider and has
// no table to resolve them with.
func model(m string) string {
	m = strings.TrimSpace(m)
	switch strings.ToLower(m) {
	case "", "inherit", "sonnet", "opus", "haiku":
		return ""
	}
	return m
}

// Plan is the permission mode of a subagent that only reads.
const Plan = "plan"

// modes are Claude Code's permission modes. Only plan changes anything in
// aish: default, manual and dontAsk are what a subagent has anyway, as it
// cannot ask and a question is a refusal for it; acceptEdits, auto and
// bypassPermissions would lift checks, which no subagent's file does here.
var modes = []string{"default", "manual", "acceptEdits", "auto", "dontAsk", "bypassPermissions", Plan}

// permissionMode is the permissionMode field as Claude Code spells it; ""
// without one. A mode aish does not know is the file's problem, not one to
// pass over: it may be one that limits the subagent.
func permissionMode(m string) (string, error) {
	m = strings.TrimSpace(m)
	if m == "" {
		return "", nil
	}
	for _, k := range modes {
		if strings.EqualFold(m, k) {
			return k, nil
		}
	}
	return "", fmt.Errorf("permissionMode %q: one of %s", m, strings.Join(modes, ", "))
}

// list is a frontmatter field that is a string or a list of them.
func list(v any) []string {
	switch v := v.(type) {
	case nil:
		return nil
	case []any:
		var out []string
		for _, s := range v {
			out = append(out, fmt.Sprint(s))
		}
		return out
	}
	return []string{fmt.Sprint(v)}
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// The tools fields are a string or a list.
	Tools           any    `yaml:"tools"`
	DisallowedTools any    `yaml:"disallowedTools"`
	PermissionMode  string `yaml:"permissionMode"`
	Model           string `yaml:"model"`
	// ignored are the fields of the file that are none of the above.
	ignored []string
}

// known are the fields of the frontmatter aish reads.
var known = map[string]bool{
	"name": true, "description": true, "tools": true, "disallowedTools": true, "permissionMode": true, "model": true,
}

// ignored are the fields of the frontmatter src that aish does not read,
// in the order of src: Claude Code has more, and a file written for it
// would get nothing of them here without a word.
func ignored(src []byte) []string {
	var doc yaml.Node
	if yaml.Unmarshal(src, &doc) != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 ||
		doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	m := doc.Content[0].Content
	for i := 0; i+1 < len(m); i += 2 {
		if k := m[i].Value; !known[k] {
			out = append(out, k)
		}
	}
	return out
}

var errNoFrontmatter = errors.New("no frontmatter: the file must start with ---")

// parse splits the file into the frontmatter, between the --- lines that
// open it, and the body, trimmed.
func parse(path string) (frontmatter, string, error) {
	var fm frontmatter
	b, err := os.ReadFile(path)
	if err != nil {
		return fm, "", err
	}
	text := strings.TrimPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\ufeff")
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return fm, "", errNoFrontmatter
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		src := []byte(strings.Join(lines[1:i], "\n"))
		if err := yaml.Unmarshal(src, &fm); err != nil {
			return fm, "", fmt.Errorf("frontmatter: %w", err)
		}
		fm.ignored = ignored(src)
		return fm, strings.TrimSpace(strings.Join(lines[i+1:], "\n")), nil
	}
	return fm, "", errors.New("frontmatter: no closing ---")
}
