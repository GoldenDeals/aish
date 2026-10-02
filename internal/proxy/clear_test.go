package proxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/bashstate"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
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

func TestClearCommand(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.run = t.TempDir()
	restore := filepath.Join(p.run, "restore.bash")
	if err := os.WriteFile(restore, []byte("cd /srv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists := func(id, ext string) bool {
		_, err := os.Stat(filepath.Join(dir, id+ext))
		return err == nil
	}
	run := func(params string) (rpc.Info, error) {
		v, err := p.handle(context.Background(), rpc.MethodClear, json.RawMessage(params))
		if err != nil {
			return rpc.Info{}, err
		}
		return v.(rpc.Info), nil
	}
	ls := session.Entry{Kind: session.KindShell, Cmd: "ls"}

	// Plain `aish clear` of an unsaved session drops it.
	sess.Append(ls)
	old := sess.ID
	info, err := run("")
	if err != nil || info.SessionID == old || info.Saved || sess.Saved() || sess.Len() != 0 {
		t.Fatalf("clear: %+v %v", info, err)
	}
	for _, ext := range []string{".jsonl", ".state", ".name", ".lock"} {
		if exists(old, ext) {
			t.Errorf("the dropped session left %s", ext)
		}
	}
	if b, _ := os.ReadFile(restore); string(b) != "cd /srv\nexport AISH_SESSION='"+info.SessionID+"'\n" {
		t.Errorf("restore.bash %q", b)
	}

	if _, err := run(`{"save":true}`); err == nil {
		t.Error("saved an empty session")
	}

	// `aish clear save NAME` keeps the journal, the shell and the name.
	p.model, p.effort = "m", "high"
	p.base = &bashstate.State{Vars: map[string]string{}}
	p.cur = &bashstate.State{Vars: map[string]string{"X": `declare -- X="1"`}, Cwd: "/srv"}
	sess.Append(ls, session.Entry{Kind: session.KindUser, Text: "hi"})
	old = sess.ID
	info, err = run(`{"save":true,"name":"probe"}`)
	if err != nil || info.SessionID == old || info.Saved {
		t.Fatalf("clear save: %+v %v", info, err)
	}
	if o, err := session.Load(dir, old); err != nil || len(o.Entries()) != 2 {
		t.Errorf("saved journal: %v", err)
	}
	st, err := session.LoadState(dir, old)
	if err != nil || st.Model != "m" || st.Effort != "high" || st.Shell.Cwd != "/srv" || st.Shell.Vars["X"] == "" {
		t.Errorf("saved state %+v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, old+".name")); strings.TrimSpace(string(b)) != "probe" {
		t.Errorf("name %q", b)
	}
	if exists(old, ".lock") {
		t.Error("the saved session is still locked")
	}

	// A taken name changes nothing.
	sess.Append(ls)
	id := sess.ID
	for _, params := range []string{`{"save_new":true,"new_name":"probe"}`, `{"new_name":"probe"}`, `{"save":true,"name":"probe"}`} {
		if _, err := run(params); err == nil || sess.ID != id || sess.Len() != 1 || sess.Saved() {
			t.Errorf("%s: %v, %s with %d entries", params, err, sess.ID, sess.Len())
		}
	}

	// `aish new NAME` makes the next session a saved one.
	info, err = run(`{"save_new":true,"new_name":"work"}`)
	if err != nil || info.SessionID == id || !info.Saved || !sess.Saved() {
		t.Fatalf("new: %+v %v", info, err)
	}
	if !exists(info.SessionID, ".name") || !exists(info.SessionID, ".lock") {
		t.Error("the new session is not named or not locked")
	}
	sess.Append(ls)
	if o, err := session.Load(dir, info.SessionID); err != nil || len(o.Entries()) != 1 {
		t.Errorf("the new session's journal: %v", err)
	}

	// Plain `aish clear` of a saved session leaves its files, unlocked.
	saved := info.SessionID
	if info, err = run(""); err != nil || info.Saved {
		t.Fatalf("clear of a saved session: %+v %v", info, err)
	}
	if !exists(saved, ".jsonl") || !exists(saved, ".name") || exists(saved, ".lock") {
		t.Error("the saved session's files")
	}

	p.asking = true
	sess.Append(ls)
	id = sess.ID
	for _, params := range []string{"", `{"save":true}`, `{"save_new":true}`} {
		if _, err := run(params); err == nil || !strings.Contains(err.Error(), "by the user") || sess.ID != id {
			t.Errorf("the assistant cleared with %q: %v", params, err)
		}
	}
}
