package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/skills"
	"github.com/inebotov/aish/internal/tools"
)

func TestParseSkillMentions(t *testing.T) {
	known := []skills.Skill{{Name: "fix-issue"}, {Name: "release-notes"}, {Name: "env"}}
	names := func(ss []skills.Skill) string {
		var out []string
		for _, s := range ss {
			out = append(out, s.Name)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct{ text, used, args string }{
		{`/fix-issue 123 "with tests"`, "fix-issue", `123 "with tests"`},
		{"Оформи задачу по багу /fix-issue.", "fix-issue", "Оформи задачу по багу"},
		{"/release-notes v2, потом /fix-issue 7 и снова /release-notes", "release-notes,fix-issue", "v2, потом 7 и снова"},
		{"запусти /usr/bin/env и /nope в /tmp", "", "запусти /usr/bin/env и /nope в /tmp"},
		{"без упоминаний", "", "без упоминаний"},
	} {
		used, args := parseSkillMentions(c.text, known)
		if names(used) != c.used || args != c.args {
			t.Errorf("%q: got %q, %q; want %q, %q", c.text, names(used), args, c.used, c.args)
		}
	}
}

// writeSkill puts a skill into dir/.claude/skills.
func writeSkill(t *testing.T, dir, name, front, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".claude", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: " + name + "\ndescription: " + name + " skill\n" + front + "---\n" + body
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSkillMentions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	proj := filepath.Join(home, "proj")
	path := writeSkill(t, proj, "fix-issue", "", "Fix $ARGUMENTS\n")
	writeSkill(t, proj, "deploy", "disable-model-invocation: true\n", "Deploy to $0\n")

	if es := skillMentions("no mentions here", proj); es != nil {
		t.Fatalf("no slash: %+v", es)
	}
	es := skillMentions("do it /fix-issue 123", proj)
	if len(es) != 1 {
		t.Fatalf("entries %+v", es)
	}
	e := es[0]
	if e.Kind != session.KindSkill || e.About != "fix-issue" || e.Path != path || e.IsError || !strings.Contains(e.Text, "Fix do it 123") {
		t.Fatalf("entry %+v", e)
	}
	if got := skillNote(e, proj); got != "/fix-issue (.claude/skills/fix-issue/SKILL.md)" {
		t.Errorf("note in the project %q", got)
	}
	if got := skillNote(e, t.TempDir()); got != "/fix-issue (~/proj/.claude/skills/fix-issue/SKILL.md)" {
		t.Errorf("note elsewhere %q", got)
	}

	// The user may invoke a skill kept from the model.
	if es := skillMentions("/deploy prod", proj); len(es) != 1 || !strings.Contains(es[0].Text, "Deploy to prod") {
		t.Fatalf("user-only skill: %+v", es)
	}

	// A skill that broke between Find and Instructions.
	bad := session.Entry{Kind: session.KindSkill, Path: path, About: "fix-issue", Text: "no frontmatter", IsError: true}
	if got := skillNote(bad, proj); got != "/fix-issue: no frontmatter" {
		t.Errorf("error note %q", got)
	}
	if got := skillsBlock([]session.Entry{bad}); !strings.Contains(got, "\n/fix-issue: no frontmatter\n</system-reminder>") {
		t.Errorf("error block %q", got)
	}
}

func TestMessagesSkill(t *testing.T) {
	m, _ := NewMasker(true, nil)
	const key = "AKIAIOSFODNN7EXAMPLE"
	es := []session.Entry{
		{Kind: session.KindFile, Path: "/p/a.go", Text: "     1\tpackage a\n"},
		{Kind: session.KindSkill, Path: "/p/.claude/skills/fix-issue/SKILL.md", About: "fix-issue", Text: "Fix 123 with " + key + "\n"},
		{Kind: session.KindUser, Text: "/fix-issue 123", Cwd: "/p"},
	}
	ms := Messages(es, 1000, m)
	if len(ms) != 1 || ms[0].Role != llm.RoleUser {
		t.Fatalf("messages %+v", ms)
	}
	txt := ms[0].Text
	file, skill, req := strings.Index(txt, "Contents of /p/a.go"), strings.Index(txt, "Skill /fix-issue:\nFix 123"), strings.Index(txt, "\n/fix-issue 123")
	if file < 0 || skill < file || req < skill {
		t.Fatalf("order: file %d, skill %d, request %d:\n%s", file, skill, req, txt)
	}
	if strings.Contains(txt, key) || !strings.Contains(txt, "AKIA***") {
		t.Errorf("skill text not masked:\n%s", txt)
	}
}

// A request that names a skill: the note goes under it, the journal gets
// the skill before the request, and the model the instructions with it.
func TestStartSkill(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "done"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	writeSkill(t, cwd, "fix-issue", "", "Fix issue $ARGUMENTS\n")
	if err := a.Start(context.Background(), "Почини /fix-issue 42", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "skill user assistant" {
		t.Fatalf("journal %s", got)
	}
	if !strings.Contains(ui.String(), "/fix-issue (.claude/skills/fix-issue/SKILL.md)") {
		t.Errorf("terminal:\n%s", ui.String())
	}
	sent := prov.requests[0].Messages[0].Text
	if !strings.Contains(sent, "Skill /fix-issue:\n") || !strings.Contains(sent, "Fix issue Почини 42\n") {
		t.Errorf("sent:\n%s", sent)
	}
	if !strings.HasSuffix(sent, "Почини /fix-issue 42") {
		t.Errorf("request text changed:\n%s", sent)
	}
}
