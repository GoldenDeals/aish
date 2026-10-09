package subagent

import (
	"path/filepath"
	"slices"
	"testing"
)

// Without a file aish has general-purpose, with all the tools, and Explore,
// limited as tools: Read, Grep, Glob, LS limits a file's subagent.
func TestBuiltins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	for _, cwd := range []string{"", filepath.Join(home, "proj")} {
		got, problems := Find(cwd)
		if len(got) != 2 || len(problems) != 0 {
			t.Fatalf("%q: %+v, %+v", cwd, got, problems)
		}
		m := byName(got)
		for _, d := range got {
			if !d.Builtin || d.File != "" || d.Root != "" || d.Project || d.Desc == "" || d.Prompt == "" ||
				d.Model != "" || !validName.MatchString(d.Name) {
				t.Errorf("%q: %+v", cwd, d)
			}
		}
		if d, ok := m["general-purpose"]; !ok || d.Tools != nil || d.Disallowed != nil || d.Mode != "" {
			t.Errorf("%q: general-purpose %+v", cwd, d)
		}
		if d, ok := m["Explore"]; !ok || !slices.Equal(d.Tools, []string{"Read", "Grep", "Glob", "LS"}) || d.Mode != "" {
			t.Errorf("%q: Explore %+v", cwd, d)
		}
	}
	// Each call has its own: a caller that changes one changes no other's.
	a, _ := Find("")
	a[0].Tools[0] = "Bash"
	if b := byName(must(Find("")))["Explore"]; b.Tools[0] != "Read" {
		t.Errorf("Explore shared: %+v", b)
	}
}

// A file of the same name, in any root, replaces aish's own subagent; one
// that is not a subagent's, or a broken one, does not.
func TestBuiltinReplaced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	proj := filepath.Join(home, "proj")
	write(t, filepath.Join(home, ".claude", "agents", "Explore.md"), agent("Explore", "user's"))
	write(t, filepath.Join(proj, ".claude", "agents", "general-purpose.md"), agent("general-purpose", "project's"))

	m := byName(must(Find(proj)))
	if d := m["Explore"]; d.Builtin || d.Desc != "user's" || d.Tools != nil || d.Prompt != "You are Explore." {
		t.Errorf("Explore: %+v", d)
	}
	if d := m["general-purpose"]; d.Builtin || d.Desc != "project's" || !d.Project {
		t.Errorf("general-purpose: %+v", d)
	}
	if d := byName(must(Find("")))["general-purpose"]; !d.Builtin {
		t.Errorf("general-purpose, user's only: %+v", d)
	}
	// Names are as Claude Code's, case and all: explore is another one.
	write(t, filepath.Join(home, "cfg", "aish", "agents", "explore.md"), agent("explore", "lower case"))
	m = byName(must(Find("")))
	if m["explore"].Desc != "lower case" || m["Explore"].Desc != "user's" || !m["general-purpose"].Builtin {
		t.Errorf("explore: %+v", m)
	}

	other := filepath.Join(home, "other")
	write(t, filepath.Join(other, ".claude", "agents", "general-purpose.md"), "# no frontmatter\n")
	write(t, filepath.Join(other, ".claude", "agents", "Explore.md"), "---\nname: Explore\n---\nno description\n")
	got, problems := Find(other)
	if len(problems) != 1 || filepath.Base(problems[0].Path) != "Explore.md" {
		t.Errorf("problems: %+v", problems)
	}
	if m := byName(got); !m["general-purpose"].Builtin || m["Explore"].Desc != "user's" {
		t.Errorf("from other: %+v", got)
	}
}
