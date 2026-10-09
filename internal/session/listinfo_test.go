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

// closed puts a session with requests and a state on disk, as a shell
// left it.
func closed(t *testing.T, dir string, cwd string, requests ...string) string {
	t.Helper()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range requests {
		if err := s.Append(Entry{Kind: KindUser, Text: r}, Entry{Kind: KindShell, Cmd: "ls", Output: "a\nb\n"}); err != nil {
			t.Fatal(err)
		}
	}
	s.Unlock()
	if err := SaveState(dir, s.ID, Saved{Shell: shellstate.State{Cwd: cwd}, TopLevel: true, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

func listed(t *testing.T, dir, id string) Info {
	t.Helper()
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range list {
		if i.ID == id {
			return i
		}
	}
	t.Fatalf("%s not listed: %+v", id, list)
	return Info{}
}

func line(t *testing.T, e Entry) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// A journal written to by anything but this List, in whatever way, is read
// again: the list shows what it holds now, not what the .info kept.
func TestListInfoStale(t *testing.T) {
	dir := t.TempDir()
	id := closed(t, dir, "/srv", "one", "two")
	journal := filepath.Join(dir, id+".jsonl")
	if i := listed(t, dir, id); i.Last != "two" || i.Requests != 2 || i.Cwd != "/srv" || i.Model != "m" || !i.TopLevel {
		t.Fatalf("first List: %+v", i)
	}
	fi, err := os.Stat(infoPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf(".info is %v, want 0600", fi.Mode().Perm())
	}

	// Appended by hand.
	f, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(line(t, Entry{Kind: KindUser, Text: "three"}))
	f.Close()
	if i := listed(t, dir, id); i.Last != "three" || i.Requests != 3 {
		t.Errorf("appended: %q, %d requests", i.Last, i.Requests)
	}

	// Edited in place to the same size, a later mtime.
	b, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), `"three"`, `"THREE"`, 1))
	fi, _ = os.Stat(journal)
	if err := os.WriteFile(journal, b, 0o600); err != nil {
		t.Fatal(err)
	}
	later := fi.ModTime().Add(time.Second)
	if err := os.Chtimes(journal, later, later); err != nil {
		t.Fatal(err)
	}
	if i := listed(t, dir, id); i.Last != "THREE" || i.Requests != 3 {
		t.Errorf("edited: %q, %d requests", i.Last, i.Requests)
	}

	// Replaced by another file of the same size and mtime, one request
	// fewer.
	b = []byte(strings.Replace(string(b), `"kind":"user","time"`, `"kind":"User","time"`, 1))
	fi, _ = os.Stat(journal)
	tmp := filepath.Join(t.TempDir(), "j")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, journal); err != nil {
		t.Fatal(err)
	}
	if i := listed(t, dir, id); i.Last != "THREE" || i.Requests != 2 {
		t.Errorf("replaced: %q, %d requests", i.Last, i.Requests)
	}
}

// A state saved since, or removed, is read again.
func TestListInfoState(t *testing.T) {
	dir := t.TempDir()
	id := closed(t, dir, "/srv", "one")
	if i := listed(t, dir, id); i.Cwd != "/srv" {
		t.Fatalf("cwd %q", i.Cwd)
	}
	if err := SaveState(dir, id, Saved{Shell: shellstate.State{Cwd: "/tmp"}, Profile: "local", Model: "q"}); err != nil {
		t.Fatal(err)
	}
	if i := listed(t, dir, id); i.Cwd != "/tmp" || i.Profile != "local" || i.Model != "q" || i.TopLevel {
		t.Errorf("saved again: %+v", i)
	}
	if err := os.Remove(statePath(dir, id)); err != nil {
		t.Fatal(err)
	}
	if i := listed(t, dir, id); i.Cwd != "" || i.Profile != "" || i.Model != "" || i.Last != "one" {
		t.Errorf("no state: %+v", i)
	}
}

// What the files are as the .info saw them is not read again.
func TestListInfoUsed(t *testing.T) {
	dir := t.TempDir()
	id := closed(t, dir, "/srv", "one")
	listed(t, dir, id)
	c := readInfo(dir, id)
	if c.Journal == nil || c.State == nil {
		t.Fatalf("not kept: %+v", c)
	}
	c.Last, c.Requests, c.Cwd = "kept", 7, "/kept"
	writeInfo(dir, id, c)
	if i := listed(t, dir, id); i.Last != "kept" || i.Requests != 7 || i.Cwd != "/kept" {
		t.Errorf("read again: %+v", i)
	}
}

// A broken .info, or one of another version, is no summary: the files are
// read and the .info written anew.
func TestListInfoBroken(t *testing.T) {
	dir := t.TempDir()
	id := closed(t, dir, "/srv", "one", "two")
	for _, bad := range []string{`{"v":1,"journal":`, `{"v":0,"last":"old","requests":9}`} {
		if err := os.WriteFile(infoPath(dir, id), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if i := listed(t, dir, id); i.Last != "two" || i.Requests != 2 || i.Cwd != "/srv" {
			t.Errorf("%s: %+v", bad, i)
		}
		if c := readInfo(dir, id); c.Journal == nil || c.Last != "two" {
			t.Errorf("%s: not written anew: %+v", bad, c)
		}
	}
}

// A List that wrote the .info after a Remove took the journal leaves none.
func TestListInfoRemoved(t *testing.T) {
	dir := t.TempDir()
	writeInfo(dir, "gone", infoFile{Version: infoVersion, Last: "x"})
	if _, err := os.Stat(infoPath(dir, "gone")); !os.IsNotExist(err) {
		t.Errorf("an .info without its journal: %v", err)
	}
}

func TestRemoveInfo(t *testing.T) {
	dir := t.TempDir()
	id := saved(t, dir, "work", 0)
	if i := listed(t, dir, id); i.Last != "work" {
		t.Fatalf("listed %+v", i)
	}
	if err := os.WriteFile(infoPath(dir, id)+".tmp", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(infoPath(dir, id)); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, id); err != nil {
		t.Fatal(err)
	}
	if f := files(t, dir, id); len(f) != 0 {
		t.Errorf("left %v", f)
	}
}

// A journal larger than what requests reads is counted in its end, as
// before there was an .info.
func TestListInfoTail(t *testing.T) {
	dir := t.TempDir()
	id := closed(t, dir, "/srv", "first")
	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(line(t, Entry{Kind: KindShell, Cmd: "cat big", Output: strings.Repeat("x", 2<<20)}))
	f.Write(line(t, Entry{Kind: KindUser, Text: "last"}))
	f.Close()
	if i := listed(t, dir, id); i.Last != "last" || i.Requests != 1 {
		t.Errorf("%q, %d requests", i.Last, i.Requests)
	}
}

// A request is found wherever its kind is in the line, and only a request.
func TestRequestsHandWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	journal := `{"kind":"user","text":"one"}
{"kind":"shell","cmd":"cat x.jsonl","output":"{\"kind\":\"user\",\"text\":\"no\"}"}
{"kind":"shell","cmd":"cat x.jsonl","output":"\"kind\":\"user\""}
{"text":"two","kind":"user"}
 {"kind":"user","text":"three"}
{"kind":"user","text":"cut
`
	if err := os.WriteFile(path, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	if last, n, err := requests(path); err != nil || last != "three" || n != 3 {
		t.Errorf("%q, %d requests, %v", last, n, err)
	}
}
