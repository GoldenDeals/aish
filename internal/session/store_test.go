package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveOnlyWhenAsked(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, s.ID+".jsonl")
	if err := s.Append(Entry{Kind: KindShell, Cmd: "ls"}, Entry{Kind: KindUser, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("an unsaved session wrote its journal: %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	if f, err := lock(dir, s.ID); err != nil {
		t.Fatalf("Lock locked an unsaved session: %v", err)
	} else {
		unlock(f)
	}
	if s.Saved() {
		t.Fatal("new session saved")
	}

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("saved twice: %v", err)
	}
	if err := s.Append(Entry{Kind: KindShell, Cmd: "pwd"}); err != nil {
		t.Fatal(err)
	}
	o, err := Open(journal)
	if err != nil {
		t.Fatal(err)
	}
	if es := o.Entries(); len(es) != 3 || es[0].Cmd != "ls" || es[2].Cmd != "pwd" || es[0].Time.IsZero() {
		t.Fatalf("journal on disk %+v", es)
	}
	if !s.Saved() || !o.Saved() {
		t.Error("a saved or opened session is not saved")
	}
	if _, err := lock(dir, s.ID); err == nil {
		t.Fatal("a saved session is not locked")
	}

	first := s.ID
	s.Clear()
	if s.ID == first || s.Saved() || s.Len() != 0 {
		t.Fatalf("after clear: %s saved %v, %d entries", s.ID, s.Saved(), s.Len())
	}
	if _, err := os.Stat(journal); err != nil {
		t.Errorf("clear removed the saved journal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, first+".lock")); !os.IsNotExist(err) {
		t.Errorf("the saved session is still locked: %v", err)
	}
	if err := s.Append(Entry{Kind: KindShell, Cmd: "ls"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, s.ID+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("the session after clear wrote its journal: %v", err)
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
	if err := Rename(dir, s.ID, "work"); err != nil {
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
