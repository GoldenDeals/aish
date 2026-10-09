package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/session"
)

const fullScreenOut = "\x1b[?1049h\x1b[Hline\r\n~\r\n\x1b[?1049l"

// editsProxy is a recorder proxy with a $AISH_RUN, its edits directory
// made.
func editsProxy(t *testing.T) (*Proxy, string) {
	t.Helper()
	p, _ := recorderProxy(t, t.TempDir())
	p.run = t.TempDir()
	dir := filepath.Join(p.run, editsDir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return p, dir
}

// leaveDiff is the plugin's part: the diff of one neovim, older than
// those left after it.
func leaveDiff(t *testing.T, dir, name, text string, age time.Duration) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func lastShell(t *testing.T, p *Proxy) session.Entry {
	t.Helper()
	es := p.sess.Entries()
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Kind == session.KindShell {
			return es[i]
		}
	}
	t.Fatalf("no command in the journal: %+v", es)
	return session.Entry{}
}

func leftDiffs(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

const diffA = "--- /w/a\n+++ /w/a\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"
const diffB = "--- /w/b\n+++ /w/b\n@@ -1 +1 @@\n-x\n+y\n"

// The diffs neovim left during the user's command go with it, in the
// order they were written, after its line; the files are gone.
func TestEditsRecorded(t *testing.T) {
	p, dir := editsProxy(t)
	p.marker(Marker{Kind: "cmd-start", Payload: "nvim a b"})
	leaveDiff(t, dir, "200-1.diff", diffB, time.Second) // the nested one, out first
	leaveDiff(t, dir, "100-1.diff", diffA, 0)
	p.output([]byte(fullScreenOut))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})

	e := lastShell(t, p)
	want := capture.FullScreen + "\n" + editsNote + "\n" + strings.TrimSuffix(diffB, "\n") +
		"\n" + editsNote + "\n" + strings.TrimSuffix(diffA, "\n")
	if e.Output != want || e.TUI || e.Cmd != "nvim a b" {
		t.Errorf("recorded %q, TUI %v; want %q", e.Output, e.TUI, want)
	}
	if left := leftDiffs(t, dir); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// Without a diff the command is recorded as before: neovim alone is the
// one line of a full-screen program, with the edits directory or not.
func TestEditsNone(t *testing.T) {
	p, dir := editsProxy(t)
	p.marker(Marker{Kind: "cmd-start", Payload: "nvim a"})
	p.output([]byte(fullScreenOut))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	if e := lastShell(t, p); e.Output != capture.FullScreen || !e.TUI {
		t.Errorf("recorded %q, TUI %v", e.Output, e.TUI)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-start", Payload: "ls"})
	p.output([]byte("a\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	if e := lastShell(t, p); e.Output != "a" || e.TUI {
		t.Errorf("recorded %q, TUI %v", e.Output, e.TUI)
	}

	q, _ := recorderProxy(t, t.TempDir()) // no $AISH_RUN at all
	q.marker(Marker{Kind: "cmd-start", Payload: "nvim a"})
	q.output([]byte(fullScreenOut))
	q.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	if e := lastShell(t, q); e.Output != capture.FullScreen || !e.TUI {
		t.Errorf("recorded %q, TUI %v", e.Output, e.TUI)
	}
}

// A big diff is kept as a big output is: its head and tail in the
// journal, max_output_bytes of them for the model.
func TestEditsTruncated(t *testing.T) {
	p, dir := editsProxy(t)
	var b strings.Builder
	b.WriteString("--- /w/big\n+++ /w/big\n@@ -1,20000 +1,20000 @@\n")
	for b.Len() < 1<<20 {
		b.WriteString("-old line of the big file\n+new line of the big file\n")
	}
	b.WriteString("+the last line\n")
	p.marker(Marker{Kind: "cmd-start", Payload: "nvim big"})
	leaveDiff(t, dir, "1-1.diff", b.String(), 0)
	p.output([]byte(fullScreenOut))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})

	e := lastShell(t, p)
	if !strings.Contains(e.Output, "bytes omitted ...]") || !strings.HasPrefix(e.Output, capture.FullScreen+"\n"+editsNote+"\n--- /w/big\n") ||
		!strings.HasSuffix(e.Output, "\n+the last line") {
		t.Errorf("recorded %q…%q", e.Output[:200], e.Output[len(e.Output)-100:])
	}
	if n := len(e.Output); n > headCap+tailCap+200 {
		t.Errorf("recorded %d bytes", n)
	}
	const max = 16000
	msgs := agent.Messages([]session.Entry{e}, max, nil)
	if len(msgs) != 1 || len(msgs[0].Text) > max+200 || !strings.Contains(msgs[0].Text, "+the last line") {
		t.Errorf("sent %d messages, %d bytes", len(msgs), len(msgs[0].Text))
	}
}

// The diff reaches the model as output does, masked; the journal keeps it
// whole.
func TestEditsMasked(t *testing.T) {
	p, dir := editsProxy(t)
	p.marker(Marker{Kind: "cmd-start", Payload: "nvim .env"})
	leaveDiff(t, dir, "1-1.diff", "--- /w/.env\n+++ /w/.env\n@@ -1 +1 @@\n-api_key=oldoldoldold\n+api_key=newnewnewnew\n", 0)
	p.output([]byte(fullScreenOut))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})

	e := lastShell(t, p)
	if !strings.Contains(e.Output, "+api_key=newnewnewnew") {
		t.Errorf("journal has %q", e.Output)
	}
	mask, err := agent.NewMasker(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs := agent.Messages([]session.Entry{e}, 16000, mask)
	if len(msgs) != 1 || strings.Contains(msgs[0].Text, "newnewnewnew") || strings.Contains(msgs[0].Text, "oldoldoldold") ||
		!strings.Contains(msgs[0].Text, "+api_key=") {
		t.Errorf("sent %+v", msgs)
	}
}

// Diffs no command of the user's took are dropped: those left before the
// command, those of the agent's commands, those of a line that is not
// recorded with its output.
func TestEditsDropped(t *testing.T) {
	p, dir := editsProxy(t)
	p.ignore = []string{"*secret*"}

	leaveDiff(t, dir, "1-1.diff", diffA, 0) // before the command
	p.marker(Marker{Kind: "cmd-start", Payload: "ls"})
	p.output([]byte("a\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	if e := lastShell(t, p); e.Output != "a" {
		t.Errorf("ls recorded %q", e.Output)
	}

	p.marker(Marker{Kind: "ask-start"})
	p.marker(Marker{Kind: "agent-start", Payload: "c1;nvim --headless +wq a"})
	leaveDiff(t, dir, "2-1.diff", diffA, 0)
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/w"})
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	p.marker(Marker{Kind: "cmd-start", Payload: "pwd"})
	p.output([]byte("/w\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	if e := lastShell(t, p); e.Output != "/w" {
		t.Errorf("pwd recorded %q", e.Output)
	}

	p.marker(Marker{Kind: "cmd-start", Payload: "nvim secret.env"})
	leaveDiff(t, dir, "3-1.diff", "--- /w/secret.env\n+++ /w/secret.env\n@@ -1 +1 @@\n-K=1\n+K=2\n", 0)
	p.output([]byte(fullScreenOut))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	if e := lastShell(t, p); e.Output != session.NotRecorded {
		t.Errorf("ignored command recorded %q", e.Output)
	}
	if left := leftDiffs(t, dir); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// The screen cleared after neovim leaves its diff: the command is
// recorded with it.
func TestEditsAfterClear(t *testing.T) {
	p, dir := editsProxy(t)
	p.marker(Marker{Kind: "cmd-start", Payload: "nvim a && clear"})
	leaveDiff(t, dir, "1-1.diff", diffA, 0)
	p.output([]byte(fullScreenOut + "\x1b[H\x1b[2J\x1b[3J"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	want := editsNote + "\n" + strings.TrimSuffix(diffA, "\n")
	if e := lastShell(t, p); e.Output != want {
		t.Errorf("recorded %q, want %q", e.Output, want)
	}
}

// What is not a diff the plugin wrote is left alone: a FIFO, which would
// hang the proxy, the plugin's file being written. Lines in another
// encoding than UTF-8 are kept, as UTF-8.
func TestEditsOddFiles(t *testing.T) {
	p, dir := editsProxy(t)
	p.marker(Marker{Kind: "cmd-start", Payload: "nvim a"})
	if err := syscall.Mkfifo(filepath.Join(dir, "9-1.diff"), 0o600); err != nil {
		t.Fatal(err)
	}
	leaveDiff(t, dir, ".9-2.tmp", diffB, 0)
	leaveDiff(t, dir, "9-3.diff", "--- /w/a\n+++ /w/a\n@@ -1 +1 @@\n-caf\xe9\n+caf\xe8\n", 0)
	p.output([]byte(fullScreenOut))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/w"})
	want := capture.FullScreen + "\n" + editsNote + "\n--- /w/a\n+++ /w/a\n@@ -1 +1 @@\n-caf�\n+caf�"
	if e := lastShell(t, p); e.Output != want {
		t.Errorf("recorded %q, want %q", e.Output, want)
	}
	if left := leftDiffs(t, dir); len(left) != 2 {
		t.Errorf("left %v, want the FIFO and the .tmp", left)
	}
}
