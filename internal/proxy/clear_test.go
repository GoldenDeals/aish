package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// Erasing the screen cuts the journal for the model; the session stays.
func TestClearedMarksJournal(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.cleared()
	if n := sess.Len(); n != 0 {
		t.Fatalf("a clear of an empty journal recorded %d entries", n)
	}
	id := sess.ID
	sess.Append(session.Entry{Kind: session.KindShell, Cmd: "ls"})
	p.cleared()
	p.cleared()
	es := sess.Entries()
	if len(es) != 2 || es[1].Kind != session.KindClear || sess.ID != id {
		t.Fatalf("after two clears: %s %+v", sess.ID, es)
	}
	if c := session.Current(es); len(c) != 0 {
		t.Errorf("the model would see %+v", c)
	}
}

// `aish clear` and `aish new NAME` leave the session on disk, as it is,
// and start one that is on disk from its first entry.
func TestClearCommand(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.run = t.TempDir()
	ctx := asUser(p)
	restore := filepath.Join(p.run, "restore.bash")
	if err := os.WriteFile(restore, []byte("cd /srv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists := func(id, ext string) bool {
		_, err := os.Stat(filepath.Join(dir, id+ext))
		return err == nil
	}
	run := func(params string) (rpc.Info, error) {
		v, err := p.handle(ctx, rpc.MethodClear, json.RawMessage(params))
		if err != nil {
			return rpc.Info{}, err
		}
		return v.(rpc.Info), nil
	}
	ls := session.Entry{Kind: session.KindShell, Cmd: "ls"}

	// A session without entries has nothing to leave on disk.
	empty := p.sess.ID
	info, err := run("")
	if err != nil || info.SessionID == empty || info.Saved || p.sess.Len() != 0 {
		t.Fatalf("clear: %+v %v", info, err)
	}
	if found, _ := filepath.Glob(filepath.Join(dir, empty+".*")); len(found) > 0 {
		t.Errorf("an empty session left %v", found)
	}
	if b, _ := os.ReadFile(restore); string(b) != "cd /srv\nexport 'AISH_SESSION="+info.SessionID+"'\n" {
		t.Errorf("restore.bash %q", b)
	}

	// One with entries stays, unlocked, for aish resume.
	p.sess.Append(ls, session.Entry{Kind: session.KindUser, Text: "hi"})
	left := p.sess.ID
	if !exists(left, ".jsonl") || !exists(left, ".lock") {
		t.Fatal("the first entry did not put the session on disk")
	}
	info, err = run("")
	if err != nil || info.SessionID == left || info.Saved || p.sess.Len() != 0 {
		t.Fatalf("clear: %+v %v", info, err)
	}
	if o, err := session.Load(dir, left); err != nil || len(o.Entries()) != 2 {
		t.Errorf("the journal left: %v", err)
	}
	if exists(left, ".lock") {
		t.Error("the session left is still locked")
	}

	// A taken name changes nothing.
	if err := session.Rename(dir, left, "probe"); err != nil {
		t.Fatal(err)
	}
	p.sess.Append(ls)
	id := p.sess.ID
	if _, err := run(`{"name":"probe"}`); err == nil || p.sess.ID != id || p.sess.Len() != 1 {
		t.Errorf("a taken name: %v, %s with %d entries", err, p.sess.ID, p.sess.Len())
	}

	// `aish new NAME` names the next session, on disk with its first entry.
	info, err = run(`{"name":"work"}`)
	if err != nil || info.SessionID == id || info.Name != "work" || info.Saved {
		t.Fatalf("new: %+v %v", info, err)
	}
	if exists(info.SessionID, ".name") || exists(info.SessionID, ".lock") {
		t.Error("a session without entries is on disk")
	}
	p.sess.Append(ls)
	if !exists(info.SessionID, ".lock") {
		t.Error("the new session is not locked")
	}
	if o, err := session.Load(dir, info.SessionID); err != nil || len(o.Entries()) != 1 || o.Name() != "work" {
		t.Errorf("the new session's journal: %v", err)
	}

	p.asking = true
	id = p.sess.ID
	for _, params := range []string{"", `{"name":"x"}`} {
		if _, err := run(params); err == nil || !strings.Contains(err.Error(), "by the user") || p.sess.ID != id {
			t.Errorf("the assistant cleared with %q: %v", params, err)
		}
	}
}
