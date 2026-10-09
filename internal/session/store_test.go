package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A session is on disk from its first entry, name and lock included;
// without entries it leaves no file. Next leaves it on disk as it is.
func TestSavedFromFirstEntry(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	onDisk := func(id string) []string {
		t.Helper()
		found, err := filepath.Glob(filepath.Join(dir, id+".*"))
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	if err := s.SetName("probe"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(); err != nil {
		t.Fatal(err)
	}
	if f := onDisk(s.ID); len(f) > 0 || s.Saved() {
		t.Fatalf("a session without entries is on disk: %v, saved %v", f, s.Saved())
	}

	if err := s.Append(Entry{Kind: KindShell, Cmd: "ls"}, Entry{Kind: KindUser, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Entry{Kind: KindShell, Cmd: "pwd"}); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, s.ID+".jsonl")
	o, err := Open(journal)
	if err != nil {
		t.Fatal(err)
	}
	if es := o.Entries(); len(es) != 3 || es[0].Cmd != "ls" || es[2].Cmd != "pwd" || es[0].Time.IsZero() {
		t.Fatalf("journal on disk %+v", es)
	}
	if !s.Saved() || !o.Saved() || o.Name() != "probe" {
		t.Errorf("saved %v, opened %v named %q", s.Saved(), o.Saved(), o.Name())
	}
	if _, err := lock(dir, s.ID); err == nil {
		t.Fatal("a session on disk is not locked")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("saved again: %v", err)
	}

	first := s.ID
	next := s.Next()
	s.Unlock()
	if next.ID == first || next.Saved() || next.Len() != 0 || next.Name() != "" {
		t.Fatalf("next: %s saved %v, %d entries, named %q", next.ID, next.Saved(), next.Len(), next.Name())
	}
	if o, err := Load(dir, first); err != nil || o.Len() != 3 || o.Name() != "probe" {
		t.Errorf("the session left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, first+".lock")); !os.IsNotExist(err) {
		t.Errorf("the session left is still locked: %v", err)
	}
	// Left without an entry, it leaves nothing.
	next.Unlock()
	if f := onDisk(next.ID); len(f) > 0 {
		t.Errorf("an empty session left %v", f)
	}
}

func TestCurrentAfterClear(t *testing.T) {
	es := []Entry{
		{Kind: KindShell, Cmd: "a"},
		{Kind: KindClear},
		{Kind: KindShell, Cmd: "b"},
		{Kind: KindUser, Text: "c"},
	}
	if c := Current(es); len(c) != 2 || c[0].Cmd != "b" {
		t.Errorf("after a clear: %+v", c)
	}
	es = append(es, Entry{Kind: KindSummary, Text: "s"}, Entry{Kind: KindShell, Cmd: "d"})
	if c := Current(es); len(c) != 2 || c[0].Kind != KindSummary {
		t.Errorf("a summary after a clear: %+v", c)
	}
	es = append(es, Entry{Kind: KindClear})
	if c := Current(es); len(c) != 0 {
		t.Errorf("a clear last: %+v", c)
	}
}

func TestCheckName(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetName("work"); err != nil {
		t.Fatal(err)
	}
	if err := CheckName(dir, "", "work"); err == nil {
		t.Error("a name taken by another session")
	}
	if err := CheckName(dir, s.ID, " work "); err != nil {
		t.Errorf("its own name: %v", err)
	}
	if err := CheckName(dir, "", "two\nlines"); err == nil {
		t.Error("two lines")
	}
	if err := CheckName(dir, "", "other"); err != nil {
		t.Errorf("a free name: %v", err)
	}
}
