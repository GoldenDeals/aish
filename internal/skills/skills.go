// Package skills brings Claude Code skills into aish. A skill is a directory
// with a SKILL.md: a frontmatter with its name and description, then
// instructions. Each skill becomes a tool: the model always sees the
// description, and the call returns the instructions with the arguments put
// in, so they enter the context only when a task needs them. Skills are only
// files: nothing runs behind them and nothing is cached.
package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/tools"
)

const maxFiles = 100

// note follows the description: a call that returns instructions instead of
// doing the work would otherwise surprise the model.
const note = "This is a skill: the call returns instructions for such tasks and the skill's files. " +
	"Call it before starting the task, then follow what it returns."

// validName is what both APIs accept as a tool name; it is a file name in
// the wrappers' directory, too.
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Skill struct {
	Name string
	Desc string
	// Dir is the skill's directory; Root is the directory its skills
	// directory is in: ~/.claude, ~/.config/aish or a project's .claude.
	Dir  string
	Root string
	// Project skills come from the working directory or a directory above it.
	Project bool
	// AllowedTools is allowed-tools of the frontmatter, as written.
	AllowedTools string
	// UserOnly skills (disable-model-invocation) are kept from the model;
	// they are still commands.
	UserOnly bool
	// Hint is argument-hint; Params are the names of arguments, for $name.
	Hint   string
	Params []string
}

// Problem is what is wrong with one SKILL.md.
type Problem struct{ Path, Msg string }

func (s Skill) File() string { return filepath.Join(s.Dir, "SKILL.md") }

// Find returns the skills that apply in cwd, by name: the user's from
// ~/.claude/skills and ~/.config/aish/skills, then those from .claude/skills
// of every directory from / down to cwd. A nearer skill replaces a farther
// one of the same name. With an empty cwd only the user's are looked for.
func Find(cwd string) ([]Skill, []Problem) {
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
	byName := map[string]Skill{}
	var problems []Problem
	// ~/.claude is also the .claude of $HOME on the way to cwd.
	seen := map[string]bool{}
	for _, r := range roots {
		dir := filepath.Join(r.dir, "skills")
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			path := filepath.Join(dir, e.Name(), "SKILL.md")
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			// A FIFO would keep load waiting in open(2) for a writer, and
			// with it every request in the directory.
			if !st.Mode().IsRegular() {
				problems = append(problems, Problem{path, "not a regular file"})
				continue
			}
			s, err := load(path)
			if err != nil {
				problems = append(problems, Problem{path, err.Error()})
				continue
			}
			if s.Name != e.Name() {
				problems = append(problems, Problem{path, fmt.Sprintf("name %q differs from the directory name", s.Name)})
			}
			s.Root, s.Project = r.dir, r.project
			byName[s.Name] = s
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

// Tool is the skill as a command that prints its instructions.
func (s Skill) Tool() tools.Tool {
	desc := "What to pass to the skill, as typed after a command: words, quoted as in a shell when they have blanks"
	if s.Hint != "" {
		desc += ". Expected: " + s.Hint
	}
	return tool{s: s, args: []tools.Arg{{Name: "arguments", Type: "string", Desc: desc, Rest: true}}}
}

// tool is a skill as the agent and `aish tool` take it. It gets no wrapper
// in $AISH_RUN/bin: a skill is typed as /name.
type tool struct {
	s    Skill
	args []tools.Arg
}

func (t tool) Name() string           { return t.s.Name }
func (t tool) Desc() string           { return t.s.Desc + "\n\n" + note }
func (t tool) Args() []tools.Arg      { return t.args }
func (t tool) Schema() map[string]any { return tools.Schema(t.args) }

func (t tool) Execute(_ context.Context, _ tools.Exec, args map[string]any, _ io.Writer) (string, error) {
	a, _ := args["arguments"].(string)
	return t.s.Instructions(a)
}

// Instructions is the body of SKILL.md, read anew, with args put in, and
// where the files it refers to are.
func (s Skill) Instructions(args string) (string, error) {
	_, body, err := parse(s.File())
	if err != nil {
		return "", err
	}
	body = s.expand(body, args)
	// The walk would not enter a symlinked skill directory.
	dir, err := filepath.EvalSymlinks(s.Dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Base directory for this skill: %s\n", dir)
	if paths := files(dir); len(paths) > 0 {
		fmt.Fprintf(&b, "Files in it:\n- %s\n", strings.Join(paths, "\n- "))
	}
	if s.AllowedTools != "" {
		fmt.Fprintf(&b, "allowed-tools: %s (a hint from the skill's author; aish does not limit tools to these)\n", s.AllowedTools)
	}
	b.WriteString("\n")
	b.WriteString(strings.NewReplacer("${CLAUDE_SKILL_DIR}", dir, "$CLAUDE_SKILL_DIR", dir).Replace(body))
	return b.String(), nil
}

// expand puts args in for $ARGUMENTS, $ARGUMENTS[N], $N and $name, the way
// Claude Code does: a single backslash keeps a placeholder as is, $N beyond
// the arguments stays, $name beyond them is empty, and args no placeholder
// took are appended.
func (s Skill) expand(body, args string) string {
	alt := `ARGUMENTS\[(\d+)\]|ARGUMENTS\b|(\d+)`
	names := slices.Clone(s.Params)
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, n := range names {
		alt += "|" + regexp.QuoteMeta(n) + `\b`
	}
	words := split(args)
	took := false
	body = regexp.MustCompile(`(\\*)\$(`+alt+`)`).ReplaceAllStringFunc(body, func(m string) string {
		token := strings.TrimLeft(m, `\`)
		slashes := m[:len(m)-len(token)]
		if slashes == `\` {
			return token
		}
		name := token[1:]
		var val string
		switch i, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "ARGUMENTS["), "]")); {
		case name == "ARGUMENTS":
			val = args
		case err == nil && i < len(words):
			val = words[i]
		case err == nil:
			return m
		default:
			if i := slices.Index(s.Params, name); i >= 0 && i < len(words) {
				val = words[i]
			}
		}
		took = true
		return slashes + val
	})
	if args != "" && !took {
		body += "\nARGUMENTS: " + args + "\n"
	}
	return body
}

// split cuts args into words as a shell would, without expanding anything;
// an unclosed quote runs to the end.
func split(args string) []string {
	var words []string
	var w strings.Builder
	in, quote := false, rune(0)
	rs := []rune(args)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'' && r != '\'', quote == '"' && r != '"' && r != '\\':
			w.WriteRune(r)
		case quote != 0 && r == quote:
			quote = 0
		case r == '\\' && i+1 < len(rs) && (quote == 0 || strings.ContainsRune(`"\$`+"`", rs[i+1])):
			i++
			w.WriteRune(rs[i])
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote == 0 && unicode.IsSpace(r):
			if in {
				words = append(words, w.String())
				w.Reset()
			}
			in = false
			continue
		default:
			w.WriteRune(r)
		}
		in = true
	}
	if in {
		words = append(words, w.String())
	}
	return words
}

func load(path string) (Skill, error) {
	fm, _, err := parse(path)
	if fm.Name == "" {
		fm.Name = filepath.Base(filepath.Dir(path))
	}
	switch {
	case err != nil:
		return Skill{}, err
	case !validName.MatchString(fm.Name):
		return Skill{}, fmt.Errorf("name %q: only letters, digits, - and _, up to 64", fm.Name)
	case strings.TrimSpace(fm.Description) == "":
		return Skill{}, errors.New("no description in the frontmatter")
	}
	s := Skill{
		Name:         fm.Name,
		Desc:         strings.TrimSpace(fm.Description),
		Dir:          filepath.Dir(path),
		AllowedTools: strings.Join(list(fm.AllowedTools), ", "),
		Hint:         fm.ArgumentHint,
		Params:       strings.Fields(strings.Join(list(fm.Arguments), " ")),
	}
	switch v := fm.DisableModelInvocation.(type) {
	case bool:
		s.UserOnly = v
	case int:
		s.UserOnly = v == 1
	case string:
		s.UserOnly = slices.Contains([]string{"true", "yes", "on", "1"}, strings.ToLower(v))
	}
	return s, nil
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
	// Strings or lists.
	AllowedTools any `yaml:"allowed-tools"`
	Arguments    any `yaml:"arguments"`
	// A boolean, which Claude Code also takes as yes/no, on/off, 1/0.
	DisableModelInvocation any `yaml:"disable-model-invocation"`
	// Not YAML: see parse.
	ArgumentHint string `yaml:"-"`
}

// parse splits SKILL.md into the frontmatter, between the --- lines that
// open the file, and the body.
func parse(path string) (frontmatter, string, error) {
	var fm frontmatter
	b, err := os.ReadFile(path)
	if err != nil {
		return fm, "", err
	}
	text := strings.TrimPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\ufeff")
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return fm, "", errors.New("no frontmatter: the file must start with ---")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		// argument-hint: [file] [format] is not YAML, but it is how
		// the hint is written; it is taken as text.
		head := slices.DeleteFunc(slices.Clone(lines[1:i]), func(l string) bool {
			v, ok := strings.CutPrefix(l, "argument-hint:")
			if v = strings.TrimSpace(v); ok && len(v) > 1 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			if ok {
				fm.ArgumentHint = v
			}
			return ok
		})
		if err := yaml.Unmarshal([]byte(strings.Join(head, "\n")), &fm); err != nil {
			return fm, "", fmt.Errorf("frontmatter: %w", err)
		}
		return fm, strings.TrimSpace(strings.Join(lines[i+1:], "\n")) + "\n", nil
	}
	return fm, "", errors.New("frontmatter: no closing ---")
}

// files returns the files of the skill besides SKILL.md, for the model to
// open or run; hidden ones are left out.
func files(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() || rel == "SKILL.md" {
			return nil
		}
		if len(out) == maxFiles {
			out = append(out, "…")
			return filepath.SkipAll
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out
}
