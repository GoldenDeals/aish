package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Open takes the id from the file name, so a journal whose name is not
// a session id is refused before it is read, as Load refuses the id.
func TestOpenBadID(t *testing.T) {
	dir := t.TempDir()
	journal := []byte(`{"kind":"user","text":"hi"}` + "\n")
	for _, id := range []string{"a b", "x.state"} {
		path := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(path, journal, 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path)
		if err == nil {
			t.Errorf("%s: opened as %q with %d entries", path, s.ID, len(s.Entries()))
			continue
		}
		if s != nil {
			t.Errorf("%s: a session with the error %v", path, err)
		}
		if want := CheckID(id); want == nil || err.Error() != want.Error() {
			t.Errorf("%s: %v, want %v", path, err, want)
		}
	}

	path := filepath.Join(dir, "20261003-120000-42.jsonl")
	if err := os.WriteFile(path, journal, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "20261003-120000-42" || !s.Saved() || len(s.Entries()) != 1 || s.Entries()[0].Text != "hi" {
		t.Errorf("opened %q saved %v with %+v", s.ID, s.Saved(), s.Entries())
	}
}
