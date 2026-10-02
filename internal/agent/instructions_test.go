package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/session"
)

func TestInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	write := func(p, s string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj := filepath.Join(home, "proj")
	sub := filepath.Join(proj, "sub")
	write(filepath.Join(home, ".claude", "CLAUDE.md"), "global")
	write(filepath.Join(proj, "CLAUDE.md"), "project")
	write(filepath.Join(sub, "CLAUDE.local.md"), "local")

	var es []session.Entry
	got := instructions(es, proj)
	if len(got) != 2 || got[0].Text != "global" || got[1].Text != "project" {
		t.Fatalf("proj: %+v", got)
	}
	es = append(es, got...)
	if got := instructions(es, proj); len(got) != 0 {
		t.Fatalf("loaded twice: %+v", got)
	}
	got = instructions(es, sub)
	if len(got) != 1 || got[0].Text != "local" {
		t.Fatalf("sub: %+v", got)
	}
	es = append(es, got...)

	write(filepath.Join(proj, "CLAUDE.md"), "project v2")
	got = instructions(es, sub)
	if len(got) != 1 || got[0].Text != "project v2" {
		t.Fatalf("changed: %+v", got)
	}

	es = append(es, session.Entry{Kind: session.KindUser, Text: "hi", Cwd: sub})
	msgs := Messages(es, 1000, nil)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "Contents of "+filepath.Join(proj, "CLAUDE.md")) ||
		!strings.HasSuffix(msgs[0].Text, "\nhi") {
		t.Fatalf("messages: %q", msgs[0].Text)
	}
}
