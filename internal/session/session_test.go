package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/shellstate"
)

func TestNextStartsNewFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for range 3 {
		if err := s.Append(Entry{Kind: KindShell, Cmd: "ls"}); err != nil {
			t.Fatal(err)
		}
		ids[s.ID] = true
		next := s.Next() // within the same second
		s.Unlock()
		s = next
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != 3 || ids[s.ID] {
		t.Fatalf("journals %v and %s, want 4 different", ids, s.ID)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 4 {
		t.Fatalf("files %v, want 4", files)
	}
	for _, f := range files {
		o, err := Open(f)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if o.ID == s.ID {
			want = 0 // saved right after the last Next
		}
		if n := len(o.Entries()); n != want {
			t.Errorf("%s has %d entries, want %d", f, n, want)
		}
	}
}

func TestOpenCountsBadLines(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.jsonl")
	journal := `{"kind":"shell","cmd":"ls"}` + "\n\n" + `{"kind":"user","te` + "\n" + `{"kind":"user","text":"hi"}` + "\n"
	if err := os.WriteFile(f, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(f)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Entries()); n != 2 {
		t.Errorf("%d entries, want 2", n)
	}
	if n := s.BadLines(); n != 1 {
		t.Errorf("%d bad lines, want 1 (a blank line is not bad)", n)
	}
}

func TestTokens(t *testing.T) {
	es := []Entry{
		{Kind: KindShell, Cmd: "cat big", Output: string(make([]byte, 100000))},
		{Kind: KindAssistant, Text: "hi", InputTokens: 5000, OutputTokens: 100},
		{Kind: KindShell, Cmd: "ls", Output: string(make([]byte, 4000))},
	}
	if got := Tokens(es, 1000); got != 5100+(2+1000+40)/4 {
		t.Errorf("tokens %d", got)
	}
	es = append(es, Entry{Kind: KindSummary, Text: string(make([]byte, 400))})
	if got := Tokens(es, 1000); got != (400+40)/4 {
		t.Errorf("after a summary: %d", got)
	}
	if c := Current(es); len(c) != 1 || c[0].Kind != KindSummary {
		t.Errorf("current %+v", c)
	}
}

func TestEntryBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		e    Entry
		want int
	}{
		// Shell output counts as far as the model is sent it.
		{"long output", Entry{Kind: KindShell, Cmd: "cat big", Output: strings.Repeat("x", 5000)}, 7 + 1000 + 40},
		// A full-screen program's output is a screen, not truncated.
		{"tui", Entry{Kind: KindShell, Cmd: "top", Output: strings.Repeat("x", 5000), TUI: true}, 3 + 5000 + 40},
		{"assistant", Entry{Kind: KindAssistant, Text: "let me look", ToolCalls: []ToolCall{
			{ID: "1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
			{ID: "2", Name: "read_file", Args: json.RawMessage(`{"path":"a"}`)},
		}}, 11 + 40 + 16 + 12},
		// A tool's result is not shell output: whole.
		{"tool result", Entry{Kind: KindToolResult, Output: strings.Repeat("x", 5000)}, 5000 + 40},
	} {
		if got := EntryBytes(c.e, 1000); got != c.want {
			t.Errorf("%s: %d bytes, want %d", c.name, got, c.want)
		}
	}
	if got := EntryBytes(Entry{Kind: KindShell, Output: strings.Repeat("x", 5000)}, 0); got != 5040 {
		t.Errorf("without max_output: %d bytes", got)
	}
}

func TestLockListFind(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir)
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	a.Append(Entry{Kind: KindUser, Text: "first"}, Entry{Kind: KindUser, Text: "deploy it"})
	if err := SaveState(dir, a.ID, Saved{Model: "m", Shell: shellstate.State{Cwd: "/srv"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := lock(dir, a.ID); err == nil {
		t.Fatal("a locked session locked twice")
	}
	if err := Rename(dir, a.ID, "deploy"); err == nil {
		t.Fatal("renamed a session another holds")
	}
	if err := a.SetName("  deploy  "); err != nil {
		t.Fatal(err)
	}

	b := &Session{ID: "20200101-000000-1", path: filepath.Join(dir, "20200101-000000-1.jsonl"), saved: true}
	b.Append(Entry{Kind: KindShell, Cmd: "ls"})
	old := time.Now().Add(-time.Hour)
	os.Chtimes(b.path, old, old)
	if err := Rename(dir, b.ID, "deploy"); err == nil {
		t.Error("two sessions with one name")
	}

	list, err := List(dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	got := list[0]
	if got.ID != a.ID || got.Name != "deploy" || !got.Open || got.Last != "deploy it" || got.Requests != 2 || got.Cwd != "/srv" || got.Model != "m" {
		t.Errorf("%+v", got)
	}
	if list[1].Open {
		t.Error("b is not open")
	}
	for q, want := range map[string]string{"deploy": a.ID, "Dep": a.ID, "2020": b.ID, b.ID: b.ID} {
		if i, err := Find(list, q); err != nil || i.ID != want {
			t.Errorf("find %q: %v %v", q, i.ID, err)
		}
	}
	if _, err := Find(list, "nope"); err == nil {
		t.Error("found nothing")
	}

	// The session after a is not locked till it is on disk.
	next := a.Next()
	a.Unlock()
	if isOpen(dir, a.ID) || isOpen(dir, next.ID) {
		t.Error("a journal stayed locked after Unlock, or one not on disk is")
	}
	if err := next.Append(Entry{Kind: KindShell, Cmd: "ls"}); err != nil || !isOpen(dir, next.ID) {
		t.Errorf("on disk, yet not locked: %v", err)
	}
	next.Unlock()
	if isOpen(dir, next.ID) {
		t.Error("still locked")
	}
}
