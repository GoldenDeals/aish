package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Latest opens the newest session no aish holds, the first of List that is
// not open, and summarizes none: no <id>.info is left behind.
func TestLatest(t *testing.T) {
	dir := t.TempDir()
	saved(t, dir, "oldest", 4*time.Hour)
	older := saved(t, dir, "older", 3*time.Hour)
	stale := saved(t, dir, "stale", 2*time.Hour)
	held := saved(t, dir, "held", time.Hour)
	// The lock file of an aish that died holds nothing.
	if err := os.WriteFile(filepath.Join(dir, stale+".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := lock(dir, held)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(l)
	// Newer than all, yet not a session.
	if err := os.WriteFile(filepath.Join(dir, "a b.jsonl"), []byte(`{"kind":"user","text":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != stale || !s.Saved() || len(s.Entries()) != 1 || s.Entries()[0].Text != "stale" {
		t.Errorf("Latest: %s with %+v, want %s", s.ID, s.Entries(), stale)
	}
	if found, _ := filepath.Glob(filepath.Join(dir, "*.info")); len(found) > 0 {
		t.Errorf("summaries written: %v", found)
	}
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(list, func(i Info) bool { return !i.Open }); i < 0 || list[i].ID != s.ID {
		t.Errorf("Latest %s, List %+v", s.ID, list)
	}

	// The one it took is held now: the next newest.
	l2, err := lock(dir, stale)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(l2)
	if s, err := Latest(dir); err != nil || s.ID != older {
		t.Errorf("Latest: %v %v, want %s", s, err, older)
	}
}

// With every session open, or none at all, Latest starts a new one.
func TestLatestNone(t *testing.T) {
	dir := t.TempDir()
	id := saved(t, dir, "held", time.Hour)
	l, err := lock(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(l)
	for _, d := range []string{dir, filepath.Join(dir, "none")} {
		s, err := Latest(d)
		if err != nil {
			t.Fatal(err)
		}
		if d == dir && s.ID == id || s.Saved() || s.Len() != 0 || s.Dir() != d {
			t.Errorf("%s: %s saved %v with %d entries in %s", d, s.ID, s.Saved(), s.Len(), s.Dir())
		}
	}
}
