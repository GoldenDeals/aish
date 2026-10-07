package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/tools"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skill(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\nDo the " + name + " thing.\n"
}

func TestFind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	user := filepath.Join(home, ".claude", "skills")
	proj := filepath.Join(home, "proj")
	sub := filepath.Join(proj, "sub")

	write(t, filepath.Join(user, "shared", "SKILL.md"), skill("shared", "user"))
	write(t, filepath.Join(proj, ".claude", "skills", "shared", "SKILL.md"), skill("shared", "project"))
	write(t, filepath.Join(sub, ".claude", "skills", "shared", "SKILL.md"), skill("shared", "sub"))
	write(t, filepath.Join(home, "cfg", "aish", "skills", "mine", "SKILL.md"), skill("mine", "aish's own"))
	write(t, filepath.Join(user, "other", "SKILL.md"), skill("renamed", "loaded anyway"))
	write(t, filepath.Join(user, "plain", "SKILL.md"), "no frontmatter\n")
	write(t, filepath.Join(user, "yaml", "SKILL.md"), "---\nname: [yaml\n---\n")
	write(t, filepath.Join(user, "nodesc", "SKILL.md"), "---\nname: nodesc\n---\nbody\n")
	write(t, filepath.Join(user, "spaced", "SKILL.md"), skill("has space", "bad name"))
	write(t, filepath.Join(user, "notes", "README.md"), "not a skill")
	write(t, filepath.Join(user, "unnamed", "SKILL.md"), "---\ndescription: named after its directory\n---\n")

	names := func(ss []Skill) map[string]Skill {
		m := map[string]Skill{}
		for _, s := range ss {
			m[s.Name] = s
		}
		return m
	}

	got, problems := Find(sub)
	m := names(got)
	if len(m) != 4 || m["unnamed"].Desc == "" || m["shared"].Desc != "sub" || !m["shared"].Project || m["mine"].Project || m["renamed"].Name == "" {
		t.Fatalf("from sub: %+v", got)
	}
	if m["renamed"].Project {
		t.Errorf("~/.claude is the user's even on the way from cwd: %+v", m["renamed"])
	}
	if got[0].Name != "mine" {
		t.Errorf("not sorted: %+v", got)
	}
	want := map[string]string{
		"other":  "differs from the directory name",
		"plain":  "no frontmatter",
		"yaml":   "frontmatter: yaml",
		"nodesc": "no description",
		"spaced": `name "has space"`,
	}
	for _, p := range problems {
		dir := filepath.Base(filepath.Dir(p.Path))
		if w, ok := want[dir]; !ok || !strings.Contains(p.Msg, w) {
			t.Errorf("unexpected problem %s: %s", p.Path, p.Msg)
		}
		delete(want, dir)
	}
	if len(want) > 0 {
		t.Errorf("problems not reported: %v", want)
	}

	if s := names(must(Find(proj)))["shared"]; s.Desc != "project" {
		t.Errorf("from proj: %+v", s)
	}
	if s := names(must(Find("")))["shared"]; s.Desc != "user" || s.Project {
		t.Errorf("user only: %+v", s)
	}
	if got, problems := Find(filepath.Join(t.TempDir(), "nowhere")); len(got) != 4 || len(problems) != 5 {
		t.Errorf("missing directories: %v %v", got, problems)
	}
}

func must(ss []Skill, _ []Problem) []Skill { return ss }

func TestInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	// A symlinked skill directory, as when skills come from a checkout.
	real := filepath.Join(t.TempDir(), "pdf")
	write(t, filepath.Join(real, "SKILL.md"), "---\n"+
		"name: pdf\n"+
		"description: >\n  Extract text\n  from PDF files.\n"+
		"license: MIT\n"+
		"metadata: {version: 2}\n"+
		"allowed-tools: [Read, \"Bash(python *)\"]\n"+
		"---\n\n"+
		"Run `python ${CLAUDE_SKILL_DIR}/scripts/extract.py FILE`; see reference.md.\n")
	write(t, filepath.Join(real, "reference.md"), "ref")
	write(t, filepath.Join(real, "scripts", "extract.py"), "print()")
	write(t, filepath.Join(real, ".git", "HEAD"), "x")
	os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755)
	if err := os.Symlink(real, filepath.Join(home, ".claude", "skills", "pdf")); err != nil {
		t.Fatal(err)
	}

	got, problems := Find(home)
	if len(got) != 1 || len(problems) != 0 {
		t.Fatalf("%+v %+v", got, problems)
	}
	tool := got[0].Tool()
	if tool.Name() != "pdf" || !strings.HasPrefix(tool.Desc(), "Extract text from PDF files.") || tools.IsHidden(tool) {
		t.Errorf("tool: %+v", tool)
	}
	out, err := tool.Execute(context.Background(), tools.Exec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"Base directory for this skill: " + real + "\n",
		"- reference.md\n- scripts/extract.py\n",
		"allowed-tools: Read, Bash(python *) (a hint",
		"\n\nRun `python " + real + "/scripts/extract.py FILE`",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("no %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, ".git") || strings.Contains(out, "SKILL.md") || strings.Contains(out, "license") {
		t.Errorf("hidden files, SKILL.md or frontmatter in:\n%s", out)
	}
}

func TestArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	dir := filepath.Join(home, ".claude", "skills")
	write(t, filepath.Join(dir, "migrate", "SKILL.md"), "---\n"+
		"description: Migrate a component\n"+
		"argument-hint: [component] [from] [to]\n"+
		"arguments: [component, from, to]\n"+
		"disable-model-invocation: yes\n"+
		"---\n"+
		"Migrate $0 ($component) from $ARGUMENTS[1] to $to, all: $ARGUMENTS.\n"+
		"Kept: $3, \\$1.00, \\$HOME, $component_x. Doubled: \\\\$1.\n")
	write(t, filepath.Join(dir, "plain", "SKILL.md"), skill("plain", "No placeholders"))
	write(t, filepath.Join(dir, "named", "SKILL.md"), "---\ndescription: d\narguments: who\n---\nHi $who, $1.\n")

	got, problems := Find("")
	if len(got) != 3 || len(problems) != 0 {
		t.Fatalf("%+v %+v", got, problems)
	}
	m, named, plain := got[0], got[1], got[2]
	if !m.UserOnly || plain.UserOnly || m.Hint != "[component] [from] [to]" || len(m.Params) != 3 {
		t.Fatalf("frontmatter: %+v", m)
	}
	tool := m.Tool()
	if u := tools.Usage(tool.Name(), tool.Args()); u != "migrate [ARGUMENTS...]" {
		t.Errorf("usage: %s", u)
	}
	args, err := tools.ParseCLI(tool.Name(), tool.Args(), []string{"Search Bar", "JS", "it's"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), tools.Exec{}, args, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "Migrate Search Bar (Search Bar) from JS to it's, all: 'Search Bar' JS 'it'\\''s'.\n" +
		"Kept: $3, $1.00, \\$HOME, $component_x. Doubled: \\\\JS.\n"
	if !strings.HasSuffix(out, "\n\n"+want) {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}

	for _, c := range []struct {
		s          Skill
		args, want string
	}{
		{plain, "", "Do the plain thing.\n"},
		{plain, "a  b", "Do the plain thing.\n\nARGUMENTS: a  b\n"},
		// $1 is past the arguments, but $who took them.
		{named, "you", "Hi you, $1.\n"},
		{named, "", "Hi , $1.\n"},
		{m, `"$ARGUMENTS" x\ y "a \"b\"" 'c d`, "Migrate $ARGUMENTS ($ARGUMENTS) from x y to a \"b\""},
	} {
		out, err := c.s.Instructions(c.args)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%s %q: %v\n%s\nwant %q", c.s.Name, c.args, err, out, c.want)
		}
	}
	if w := split(`'c d`); len(w) != 1 || w[0] != "c d" {
		t.Errorf("unclosed quote: %q", w)
	}
}
