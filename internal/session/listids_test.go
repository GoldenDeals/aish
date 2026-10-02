package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A journal whose name is not a session id is left alone: Load would refuse
// it, so neither the list nor --resume may pick it, however recent.
func TestListBadIDs(t *testing.T) {
	dir := t.TempDir()
	good := saved(t, dir, "good", time.Hour)
	bad := []string{"a b", "-x", "a.b", "x.state"}
	for _, id := range bad {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(`{"kind":"user","text":"x"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != good {
		t.Errorf("List: %+v, want only %s", list, good)
	}
	s, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != good {
		t.Errorf("Latest: %s, want %s", s.ID, good)
	}
	for _, id := range bad {
		if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
}
