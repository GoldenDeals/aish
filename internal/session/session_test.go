package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/bashstate"
)

func TestClearStartsNewFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for range 3 {
		if err := s.Append(Entry{Kind: KindShell, Cmd: "ls"}); err != nil {
			t.Fatal(err)
		}
		ids[s.ID] = true
		s.Clear() // within the same second
	}
	if len(ids) != 3 {
		t.Fatalf("journals %v, want 3 different", ids)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		o, err := Open(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(o.Entries()); n != 1 {
			t.Errorf("%s has %d entries, want 1", f, n)
		}
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

func TestLockListFind(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir)
	if err := a.Lock(); err != nil {
		t.Fatal(err)
	}
	a.Append(Entry{Kind: KindUser, Text: "first"}, Entry{Kind: KindUser, Text: "deploy it"})
	if err := SaveState(dir, a.ID, Saved{Model: "m", Shell: bashstate.State{Cwd: "/srv"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := lock(dir, a.ID); err == nil {
		t.Fatal("a locked session locked twice")
	}
	if err := Rename(dir, a.ID, "  deploy  "); err != nil {
		t.Fatal(err)
	}

	b := &Session{ID: "20200101-000000-1", path: filepath.Join(dir, "20200101-000000-1.jsonl")}
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

	// The lock follows the journal that Clear starts.
	first := a.ID
	a.Clear()
	if isOpen(dir, first) || !isOpen(dir, a.ID) {
		t.Error("the lock stayed with the old journal")
	}
	a.Unlock()
	if isOpen(dir, a.ID) {
		t.Error("still locked")
	}
}
