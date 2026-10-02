package subagent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func agent(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\nYou are " + name + ".\n"
}

func byName(ds []Def) map[string]Def {
	m := map[string]Def{}
	for _, d := range ds {
		m[d.Name] = d
	}
	return m
}

func TestFind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	user := filepath.Join(home, ".claude", "agents")
	proj := filepath.Join(home, "proj")
	sub := filepath.Join(proj, "sub")

	write(t, filepath.Join(user, "shared.md"), agent("shared", "user"))
	write(t, filepath.Join(proj, ".claude", "agents", "shared.md"), agent("shared", "project"))
	write(t, filepath.Join(sub, ".claude", "agents", "shared.md"), agent("shared", "sub"))
	write(t, filepath.Join(home, "cfg", "aish", "agents", "mine.md"), agent("mine", "aish's own"))
	write(t, filepath.Join(user, "other.md"), agent("renamed", "loaded anyway"))
	write(t, filepath.Join(user, "plain.md"), "no frontmatter\n")
	write(t, filepath.Join(user, "README.md"), "# Agents\n\n---\n")
	write(t, filepath.Join(user, "yaml.md"), "---\nname: [yaml\n---\nbody\n")
	write(t, filepath.Join(user, "nodesc.md"), "---\nname: nodesc\n---\nbody\n")
	write(t, filepath.Join(user, "empty.md"), "---\nname: empty\ndescription: no prompt\n---\n\n  \n")
	write(t, filepath.Join(user, "spaced.md"), agent("has space", "bad name"))
	write(t, filepath.Join(user, "unnamed.md"), "---\ndescription: named after its file\n---\nPrompt.\n")
	write(t, filepath.Join(user, "notes.txt"), "not a subagent")
	write(t, filepath.Join(user, "nested", "deep.md"), agent("deep", "not looked for"))

	got, problems := Find(sub)
	m := byName(got)
	if len(m) != 4 || m["unnamed"].Desc == "" || m["mine"].Project || m["renamed"].Project {
		t.Fatalf("from sub: %+v", got)
	}
	// A note without a frontmatter is not a subagent, not a broken one.
	if _, ok := m["plain"]; ok {
		t.Errorf("plain.md loaded: %+v", got)
	}
	if s := m["shared"]; s.Desc != "sub" || !s.Project || s.Root != filepath.Join(sub, ".claude") ||
		s.File != filepath.Join(sub, ".claude", "agents", "shared.md") {
		t.Errorf("nearest shared: %+v", s)
	}
	if s := m["mine"]; s.Root != filepath.Join(home, "cfg", "aish") {
		t.Errorf("aish's own: %+v", s)
	}
	if s := m["renamed"]; s.Root != filepath.Join(home, ".claude") || s.Prompt != "You are renamed." {
		t.Errorf("renamed: %+v", s)
	}
	if got[0].Name != "mine" {
		t.Errorf("not sorted: %+v", got)
	}
	want := map[string]string{
		"other.md":  "differs from the file name",
		"yaml.md":   "frontmatter: yaml",
		"nodesc.md": "no description",
		"empty.md":  "no system prompt",
		"spaced.md": `name "has space"`,
	}
	for _, p := range problems {
		f := filepath.Base(p.Path)
		if w, ok := want[f]; !ok || !strings.Contains(p.Msg, w) {
			t.Errorf("unexpected problem %s: %s", p.Path, p.Msg)
		}
		delete(want, f)
	}
	if len(want) > 0 {
		t.Errorf("problems not reported: %v", want)
	}

	if s := byName(must(Find(proj)))["shared"]; s.Desc != "project" || !s.Project {
		t.Errorf("from proj: %+v", s)
	}
	if s := byName(must(Find("")))["shared"]; s.Desc != "user" || s.Project {
		t.Errorf("user only: %+v", s)
	}
}

func must(ds []Def, _ []Problem) []Def { return ds }

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		head  string
		tools []string
		model string
	}{
		{"tools: Read, Bash", []string{"Read", "Bash"}, ""},
		{"tools: [Read, Bash]", []string{"Read", "Bash"}, ""},
		{"tools:\n  - Read\n  - \"Bash\"", []string{"Read", "Bash"}, ""},
		{"tools: \"\"", nil, ""},
		{"model: inherit", nil, ""},
		{"model: sonnet", nil, ""},
		{"model: haiku", nil, ""},
		{"model: gpt-5", nil, "gpt-5"},
	} {
		path := filepath.Join(dir, "reviewer.md")
		write(t, path, "---\r\nname: reviewer\r\ndescription: >\r\n  Reviews\r\n  diffs.\r\n"+
			strings.ReplaceAll(c.head, "\n", "\r\n")+"\r\n---\r\n\r\nYou review code.\r\n\r\nBe brief.\r\n")
		d, err := load(path)
		if err != nil {
			t.Errorf("%q: %v", c.head, err)
			continue
		}
		if !slices.Equal(d.Tools, c.tools) || (c.tools == nil) != (d.Tools == nil) || d.Model != c.model {
			t.Errorf("%q: tools %q, model %q", c.head, d.Tools, d.Model)
		}
		if d.Name != "reviewer" || d.Desc != "Reviews diffs." || d.Prompt != "You review code.\n\nBe brief." || d.File != path {
			t.Errorf("%q: %+v", c.head, d)
		}
	}
}
