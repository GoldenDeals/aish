package subagent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// disallowedTools is read as tools is, permissionMode as Claude Code spells
// it, and the fields aish does not read are named, not a problem.
func TestLimitsFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	dir := filepath.Join(home, ".claude", "agents")
	write(t, filepath.Join(dir, "nobash.md"), "---\nname: nobash\ndescription: d\n"+
		"disallowedTools: Bash, Write, Edit\npermissionMode: plan\nhooks:\n  Stop: []\ncolor: red\nmaxTurns: 3\n---\nBody.\n")
	write(t, filepath.Join(dir, "listed.md"), "---\ndescription: d\ntools: [Bash(git log, git diff), Read]\n"+
		"disallowedTools: [Bash(git push *, git commit *), mcp__github]\npermissionMode: BypassPermissions\n---\nBody.\n")
	write(t, filepath.Join(dir, "plain.md"), "---\ndescription: d\nmodel: inherit\n---\nBody.\n")
	write(t, filepath.Join(dir, "badmode.md"), "---\ndescription: d\npermissionMode: readonly\n---\nBody.\n")
	write(t, filepath.Join(dir, "listmode.md"), "---\ndescription: d\npermissionMode: [plan]\n---\nBody.\n")

	got, problems := Find("")
	m := byName(got)
	if d := m["nobash"]; !slices.Equal(d.Disallowed, []string{"Bash", "Write", "Edit"}) || d.Mode != Plan ||
		!slices.Equal(d.Ignored, []string{"hooks", "color", "maxTurns"}) || d.Tools != nil {
		t.Errorf("nobash: %+v", d)
	}
	if d := m["listed"]; !slices.Equal(d.Disallowed, []string{"Bash(git push *,git commit *)", "mcp__github"}) ||
		d.Mode != "bypassPermissions" || d.Ignored != nil || len(d.Tools) != 2 {
		t.Errorf("listed: %+v", d)
	}
	if d := m["plain"]; d.Disallowed != nil || d.Mode != "" || d.Ignored != nil {
		t.Errorf("plain: %+v", d)
	}
	// A mode aish does not know may be one that limits: the file is not
	// taken as one without limits.
	for _, name := range []string{"badmode", "listmode"} {
		if _, ok := m[name]; ok {
			t.Errorf("%s loaded: %+v", name, m[name])
		}
	}
	want := map[string]string{"badmode.md": `permissionMode "readonly": one of default, manual,`, "listmode.md": "frontmatter: yaml"}
	if len(problems) != len(want) {
		t.Errorf("problems %+v", problems)
	}
	for _, p := range problems {
		if w := want[filepath.Base(p.Path)]; w == "" || !strings.Contains(p.Msg, w) {
			t.Errorf("problem %s: %q, want %q", p.Path, p.Msg, w)
		}
	}
}

func TestPermissionModes(t *testing.T) {
	for in, want := range map[string]string{
		"": "", " plan ": Plan, "PLAN": Plan, "default": "default", "manual": "manual", "dontask": "dontAsk",
		"acceptEdits": "acceptEdits", "auto": "auto", "bypassPermissions": "bypassPermissions",
	} {
		if got, err := permissionMode(in); got != want || err != nil {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"readOnly", "plan mode", "yolo"} {
		if got, err := permissionMode(in); err == nil {
			t.Errorf("%q: %q, no error", in, got)
		}
	}
}
