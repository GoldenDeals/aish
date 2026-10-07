package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/session"
)

func TestParseMentions(t *testing.T) {
	got := parseMentions(`Изучи @*.md, @a.go. и @"my notes.txt" (не user@host), снова @*.md`)
	want := []string{"*.md", "a.go", "my notes.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMentions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "one\ntwo\n")
	write("b.md", "")
	write(".hidden.md", "secret")
	write("docs/c.md", "deep")
	write(".git/d.md", "git")
	write("bin.md", "x\x00y")
	write("main.go", "package main")

	paths := func(es []session.Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, mentionNote(e, dir))
		}
		return out
	}
	cases := []struct {
		text string
		want []string
	}{
		{"@*.md", []string{"@a.md (2 lines)", "@b.md (0 lines)", "@bin.md: binary file, not attached"}},
		{"@**/*.md", []string{"@a.md (2 lines)", "@b.md (0 lines)", "@bin.md: binary file, not attached", "@docs/c.md (1 line)"}},
		{"@main.go and @docs", []string{"@main.go (1 line)", "@docs/"}},
		{"@nope.txt @*.rs", []string{"@nope.txt: no such file", "@*.rs: no such file"}},
		{"@.git/*.md", []string{"@.git/d.md (1 line)"}},
	}
	for _, c := range cases {
		if got := paths(mentions(c.text, dir)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.text, got, c.want)
		}
	}

	es := mentions("@a.md", dir)
	if want := "     1\tone\n     2\ttwo\n"; es[0].Text != want {
		t.Errorf("text %q, want %q", es[0].Text, want)
	}
	block := filesBlock(es)
	if !strings.Contains(block, "Contents of "+filepath.Join(dir, "a.md")+":\n     1\tone") {
		t.Errorf("block:\n%s", block)
	}
}

func TestMentionTruncated(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("0123456789\n", 10000)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644)
	es := mentions("@big.txt", dir)
	if len(es[0].Text) > maxInstructionBytes+100 || !strings.Contains(es[0].Text, "[truncated; continue with read_file offset=") {
		t.Fatalf("not truncated: %d bytes", len(es[0].Text))
	}
	if n := mentionNote(es[0], dir); !strings.HasSuffix(n, ", truncated)") {
		t.Errorf("note %q", n)
	}
}
