package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckID(t *testing.T) {
	for _, id := range []string{"20261002-211700-1234", "20261002-211700-1234-2", "my.session", "a_b"} {
		if err := CheckID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
	for _, id := range []string{"", ".", "..", "../x", "a/b", "/x", ".hidden", "-x", "a b", "x;id", "a'b", "a\nb", "ä"} {
		if err := CheckID(id); err == nil {
			t.Errorf("%q passed", id)
		}
	}
}

// An id that leads out of the sessions directory touches nothing there.
func TestBadIDPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "x.jsonl")
	if err := os.WriteFile(outside, []byte(`{"kind":"user","text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Load(dir, "../x"); err == nil {
		t.Errorf("loaded %s", s.ID)
	}
	if err := Remove(dir, "../x"); err == nil {
		t.Error("removed ../x")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the journal outside: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x.lock")); err == nil {
		t.Error("a lock made outside")
	}
}
