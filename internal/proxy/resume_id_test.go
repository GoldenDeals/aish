package proxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

// The id of rpc resume becomes a path and a word of the script the shell
// runs at its next prompt: one that is not a plain file name is refused
// before either, even when a journal is there for it.
func TestResumeID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	other, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Append(session.Entry{Kind: session.KindUser, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	other.Unlock()

	sess, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.run = t.TempDir()
	restore := filepath.Join(p.run, "restore.bash")
	resume := func(id string) (rpc.Info, error) {
		b, _ := json.Marshal(rpc.ResumeParams{ID: id})
		v, err := p.handle(context.Background(), rpc.MethodResume, b)
		if err != nil {
			return rpc.Info{}, err
		}
		return v.(rpc.Info), nil
	}

	if err := os.Mkdir(filepath.Join(dir, "x;touch pwned;#"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../evil", "x;touch pwned;#/../../evil", "a'b"} {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(`{"kind":"user","text":"x"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := resume(id); err == nil {
			t.Errorf("resumed %q", id)
		}
		if _, err := os.Stat(restore); err == nil {
			t.Fatalf("%q wrote restore.bash", id)
		}
		if p.sess != sess {
			t.Fatalf("%q switched the session", id)
		}
	}

	info, err := resume(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != other.ID {
		t.Errorf("resumed %s, want %s", info.SessionID, other.ID)
	}
	b, err := os.ReadFile(restore)
	if err != nil {
		t.Fatal(err)
	}
	if want := "export AISH_SESSION='" + other.ID + "'\n"; !strings.HasSuffix(string(b), want) {
		t.Errorf("restore.bash %q, want it to end with %q", b, want)
	}
}
