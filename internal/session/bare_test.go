package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// spawned puts on disk a session as a `&NAME` at the prompt of a new
// shell leaves it: what the subagent spent, a Ctrl+L, more spent.
func spawned(t *testing.T, dir string, at time.Time) *Session {
	t.Helper()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{subTurn(at, "m", 100, 40, 10), {Kind: KindClear}, subTurn(at, "m", 50, 0, 5)} {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if !s.Saved() {
		t.Fatal("not saved by its first entry")
	}
	s.Unlock()
	return s
}

func listedIDs(t *testing.T, dir string) []string {
	t.Helper()
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range list {
		ids = append(ids, i.ID)
	}
	return ids
}

func latestID(t *testing.T, dir string) string {
	t.Helper()
	s, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s.ID
}

// A session where only `&NAME` was run is in neither `aish resume --all`
// nor `aish --resume`, though it is the newest; aish stats counts what it
// spent. A name from the user shows it, a request in it too.
func TestBareSession(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	older := saved(t, dir, "older", time.Hour)
	s := spawned(t, dir, now)

	// From the journal, then from the <id>.info the first List wrote.
	for n := range 2 {
		if ids := listedIDs(t, dir); !slices.Equal(ids, []string{older}) {
			t.Errorf("List %d: %v, want %s alone", n, ids, older)
		}
	}
	if id := latestID(t, dir); id != older {
		t.Errorf("Latest %s, want %s", id, older)
	}

	st, err := CollectStats(dir, Periods, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := st.Total[0], (Spend{Requests: 1, Sessions: 2, Input: 150, Cached: 40, Output: 15}); got != want {
		t.Errorf("stats %+v, want %+v", got, want)
	}

	// The user named it: it is theirs to see, and the name is taken.
	if err := Rename(dir, s.ID, "spawned"); err != nil {
		t.Fatal(err)
	}
	if ids := listedIDs(t, dir); !slices.Equal(ids, []string{s.ID, older}) {
		t.Errorf("named: List %v", ids)
	}
	if id := latestID(t, dir); id != s.ID {
		t.Errorf("named: Latest %s, want %s", id, s.ID)
	}
	if err := Rename(dir, s.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ids := listedIDs(t, dir); !slices.Equal(ids, []string{older}) {
		t.Errorf("unnamed again: List %v", ids)
	}

	// A request in it, as when the shell goes on in it.
	r, err := Load(dir, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(Entry{Kind: KindUser, Text: "go on"}); err != nil {
		t.Fatal(err)
	}
	r.Unlock()
	if i := listed(t, dir, s.ID); i.Requests != 1 || i.Last != "go on" {
		t.Errorf("with a request: %+v", i)
	}
	if id := latestID(t, dir); id != s.ID {
		t.Errorf("with a request: Latest %s, want %s", id, s.ID)
	}
}

// What an aish from before bare sessions kept in <id>.info does not show
// one: the file is of another version.
func TestBareSessionOldInfo(t *testing.T) {
	dir := t.TempDir()
	s := spawned(t, dir, time.Now())
	fi, err := os.Stat(filepath.Join(dir, s.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	js, ss := stampOf(fi), absent
	writeInfo(dir, s.ID, infoFile{Version: 1, Journal: &js, State: &ss})
	if ids := listedIDs(t, dir); len(ids) != 0 {
		t.Errorf("List %v", ids)
	}
}

// Prune takes a bare session with the rest: List does not show it, and
// nothing else would remove it.
func TestPruneBare(t *testing.T) {
	dir := t.TempDir()
	s := spawned(t, dir, time.Now())
	removed, err := Prune(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{s.ID}) || len(files(t, dir, s.ID)) != 0 {
		t.Errorf("removed %v, left %v", removed, files(t, dir, s.ID))
	}
}

// bare goes by the start of each line, as Append writes it, and stops at
// the first entry of another kind.
func TestBare(t *testing.T) {
	usage := string(line(t, subTurn(time.Now(), "m", 1, 0, 1)))
	clear := string(line(t, Entry{Kind: KindClear}))
	long := string(line(t, Entry{Kind: KindUsage, About: strings.Repeat("a", 10<<10), Usage: &Usage{Input: 1}}))
	shell := string(line(t, Entry{Kind: KindShell, Cmd: "ls", Output: strings.Repeat("x\n", 10<<10)}))
	for _, c := range []struct {
		name, journal string
		want          bool
	}{
		{"empty", "", false},
		{"blank lines", "\n \n", false},
		{"usage", usage, true},
		{"usage and clears", clear + usage + "\n" + clear + usage, true},
		{"no newline at the end", usage + strings.TrimSuffix(usage, "\n"), true},
		{"a long one", usage + long + usage, true},
		{"a command after", usage + shell + usage, false},
		{"a command first", shell + usage, false},
		{"a request", usage + string(line(t, Entry{Kind: KindUser, Text: "x"})), false},
		{"its kind not first", usage + `{"time":"2026-10-09T00:00:00Z","kind":"usage"}` + "\n", false},
		{"not JSON", usage + "{\"kind\":\"user\",\"text\":\"cut sh\n", false},
	} {
		path := filepath.Join(t.TempDir(), "20261009-000000-1.jsonl")
		if err := os.WriteFile(path, []byte(c.journal), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := bare(path); err != nil || got != c.want {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	if _, err := bare(filepath.Join(t.TempDir(), "none.jsonl")); err == nil {
		t.Error("no error for no journal")
	}
}
