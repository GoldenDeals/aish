package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// The recorder's scenarios, fed as the shell feeds them: the output and
// the markers in the order Filter reports them, no PTY.

func recorderProxy(t *testing.T, dir string) (*Proxy, *terminal) {
	t.Helper()
	sess, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	return p, out
}

func shellCmds(sess *session.Session) []string {
	var cmds []string
	for _, e := range sess.Entries() {
		if e.Kind == session.KindShell {
			cmds = append(cmds, e.Cmd)
		}
	}
	return cmds
}

// The agent's command cut short by Ctrl+C: the shell is back at its
// prompt without agent-end. The command ends interrupted on the screen,
// its output waits for the next request, which closes the call with it,
// and the journal gets none of it: it is the request's.
func TestRecorderInterruptedAgentCommand(t *testing.T) {
	p, out := recorderProxy(t, t.TempDir())
	p.marker(Marker{Kind: "ask-start"})
	p.mu.Lock()
	p.handed = "c1"
	p.mu.Unlock()
	p.marker(Marker{Kind: "agent-start", Payload: "c1;seq 1 3"})
	p.output([]byte("1\r\n2\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "130;/tmp"})

	if s := out.String(); !strings.Contains(s, "interrupted") || !strings.HasSuffix(s, "\r\n") {
		t.Errorf("terminal %q: the fold does not end interrupted", s)
	}
	p.mu.Lock()
	asking, handed, running, watching := p.asking, p.handed, len(p.agent), p.watch != nil
	folds := append([]Fold{}, p.folds...)
	p.mu.Unlock()
	if asking || handed != "" || running != 0 || watching {
		t.Errorf("after the prompt: asking %v, handed %q, %d commands running, watched %v", asking, handed, running, watching)
	}
	if len(folds) != 1 || folds[0].Title != "❯ seq 1 3" || folds[0].Text != "1\r\n2\r\n" {
		t.Errorf("folds for Ctrl+O %+v", folds)
	}
	got, err := p.wait(context.Background(), "c1", time.Millisecond)
	if err != nil || got.Exit != 130 || got.Output != "1\n2" {
		t.Errorf("output for the next request %+v, %v", got, err)
	}
	if cmds := shellCmds(p.sess); len(cmds) != 0 {
		t.Errorf("journal holds %q", cmds)
	}

	// With hide_work, a command handed off and never run: the agent's line
	// of calls, left open for it, ends at the prompt.
	out.b.Reset()
	p.marker(Marker{Kind: "ask-start"})
	(&ui{p: p}).HideCommand(nil)
	p.marker(Marker{Kind: "cmd-end", Payload: "130;/tmp"})
	if s := out.String(); !strings.Contains(s, "(interrupted)") {
		t.Errorf("terminal %q: the hidden line does not end", s)
	}
	p.mu.Lock()
	hide, waits := p.hide, p.waits
	p.mu.Unlock()
	if hide || waits {
		t.Errorf("hide %v, waits %v after the prompt", hide, waits)
	}
}

// The user's command erases the screen in the middle of its output: the
// journal is cut there for the model, and the command keeps only what is
// left on the screen. `clear` itself leaves nothing to record; a request
// that erases the screen cuts nothing.
func TestRecorderClearMidOutput(t *testing.T) {
	p, _ := recorderProxy(t, t.TempDir())
	sess := p.sess
	sess.Append(session.Entry{Kind: session.KindShell, Cmd: "echo old", Output: "old\n"})
	p.mu.Lock()
	p.folds = []Fold{{Title: "❯ ls", Text: "a\n"}}
	p.mu.Unlock()

	p.marker(Marker{Kind: "cmd-start", Payload: "build"})
	p.output([]byte("before\r\n"))
	p.output([]byte("\x1b[H\x1b[2Jafter\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "2;/srv"})

	es := sess.Entries()
	if len(es) != 3 || es[1].Kind != session.KindClear {
		t.Fatalf("journal %+v", es)
	}
	if e := es[2]; e.Cmd != "build" || e.Output != "after" || e.Exit != 2 || e.Cwd != "/srv" {
		t.Errorf("the command recorded as %+v", e)
	}
	if c := session.Current(es); len(c) != 1 || c[0].Cmd != "build" {
		t.Errorf("the model would see %+v", c)
	}
	p.mu.Lock()
	folds := len(p.folds)
	p.mu.Unlock()
	if folds != 0 {
		t.Errorf("%d folds of the erased screen kept for Ctrl+O", folds)
	}

	p.marker(Marker{Kind: "cmd-start", Payload: "clear"})
	p.output([]byte("\x1b[H\x1b[2J\x1b[3J"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/srv"})
	if es := sess.Entries(); len(es) != 4 || es[3].Kind != session.KindClear {
		t.Errorf("after clear: %+v", es)
	}

	p.marker(Marker{Kind: "cmd-start", Payload: "ls"})
	p.output([]byte("a\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/srv"})
	p.marker(Marker{Kind: "ask-start"})
	p.output([]byte("\x1b[2J"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/srv"})
	if es := sess.Entries(); len(es) != 5 || es[4].Cmd != "ls" {
		t.Errorf("a request's clear cut the journal: %+v", es)
	}
}

// `aish resume` switches the session while it runs as a command: it is
// recorded in neither session, and the next command goes to the one
// resumed.
func TestRecorderResumeDuringCommand(t *testing.T) {
	dir := t.TempDir()
	// Saved first: a session made in the same second would take its ID.
	other, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	other.Append(session.Entry{Kind: session.KindUser, Text: "hi"})
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	other.Unlock()
	p, _ := recorderProxy(t, dir)
	p.run = t.TempDir()
	old := p.sess
	old.Append(session.Entry{Kind: session.KindShell, Cmd: "ls"})

	p.marker(Marker{Kind: "cmd-start", Payload: "aish resume " + other.ID})
	p.output([]byte("resuming\r\n"))
	b, _ := json.Marshal(rpc.ResumeParams{ID: other.ID})
	if _, err := p.handle(asUser(p), rpc.MethodResume, b); err != nil {
		t.Fatal(err)
	}
	p.output([]byte("done\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/home"})

	p.mu.Lock()
	now, switched := p.sess, p.switched
	p.mu.Unlock()
	if now.ID != other.ID || switched {
		t.Fatalf("session %s, switched %v; want %s", now.ID, switched, other.ID)
	}
	if cmds := shellCmds(old); len(cmds) != 1 {
		t.Errorf("the session left recorded %q", cmds)
	}
	if cmds := shellCmds(now); len(cmds) != 0 {
		t.Errorf("the session resumed recorded %q", cmds)
	}

	p.marker(Marker{Kind: "cmd-start", Payload: "pwd"})
	p.output([]byte("/home\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/home"})
	if cmds := shellCmds(now); len(cmds) != 1 || cmds[0] != "pwd" {
		t.Errorf("the session resumed recorded %q after it", cmds)
	}
	if cmds := shellCmds(old); len(cmds) != 1 {
		t.Errorf("the session left recorded %q after it", cmds)
	}
}
