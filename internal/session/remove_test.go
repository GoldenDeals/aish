package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// saved puts a closed session with a name and a state on disk, last
// modified age ago.
func saved(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Entry{Kind: KindUser, Text: name}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Unlock()
	if err := SaveState(dir, s.ID, Saved{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := Rename(dir, s.ID, name); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(dir, s.ID+".jsonl"), when, when); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

func files(t *testing.T, dir, id string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(dir, id+".*"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	id := saved(t, dir, "work", 0)
	// Left behind by an aish that died.
	if err := os.WriteFile(filepath.Join(dir, id+".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if n := len(files(t, dir, id)); n != 4 {
		t.Fatalf("%d files before, want 4: %v", n, files(t, dir, id))
	}
	if err := Remove(dir, id); err != nil {
		t.Fatal(err)
	}
	if f := files(t, dir, id); len(f) != 0 {
		t.Errorf("left %v", f)
	}
	if err := Remove(dir, id); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Errorf("removed twice: %v", err)
	}
}

func TestRemoveOpen(t *testing.T) {
	dir := t.TempDir()
	id := saved(t, dir, "work", 0)
	s, err := Load(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	if err := Remove(dir, id); err == nil || !strings.Contains(err.Error(), "is open") {
		t.Errorf("an open session: %v", err)
	}
	if n := len(files(t, dir, id)); n != 4 {
		t.Errorf("%d files left of an open session, want 4: %v", n, files(t, dir, id))
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	day := 24 * time.Hour
	fresh := saved(t, dir, "fresh", time.Hour)
	old := saved(t, dir, "old", 40*day)
	openOld := saved(t, dir, "open", 40*day)
	s, err := Load(dir, openOld)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()

	if got, err := Prune(dir, -day); err != nil || len(got) != 0 {
		t.Fatalf("a negative ttl: %v, %v", got, err)
	}
	got, err := Prune(dir, 30*day)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{old}) {
		t.Errorf("pruned %v, want %v", got, []string{old})
	}
	list, _ := List(dir)
	var left []string
	for _, i := range list {
		left = append(left, i.ID)
	}
	slices.Sort(left)
	want := []string{fresh, openOld}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Errorf("left %v, want %v", left, want)
	}

	if got, err := Prune(dir, 0); err != nil || !slices.Equal(got, []string{fresh}) {
		t.Errorf("ttl 0: %v, %v; want all but the open one", got, err)
	}
}
