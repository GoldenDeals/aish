package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/session"
)

// An interactive session (ssh, `kubectl exec -it`) goes to the journal as
// its text once it is over, a full-screen program in it as one line; a
// command that is a full-screen program as a whole is that line, TUI.
func TestRecorderInteractiveSession(t *testing.T) {
	p, _ := recorderProxy(t, t.TempDir())
	p.marker(Marker{Kind: "cmd-start", Payload: "ssh remote"})
	for _, b := range []string{
		"Welcome to remote\r\n\x1b]0;user@remote: ~\a\x1b[1;32muser@remote\x1b[0m:~$ ",
		"lss\b\x1b[K\r\n", "a.txt\r\n",
		"user@remote:~$ vim a.txt\r\n\x1b[?10", "49h\x1b[Hline one\r\n~\r\n~",
		"\x1b[24;1H:q\r\x1b[?1049l",
		"user@remote:~$ cat a.txt\r\nline one\r\n",
		"user@remote:~$ exit\r\nlogout\r\nConnection to remote closed.\r\n",
	} {
		p.output([]byte(b))
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/home/u"})

	p.marker(Marker{Kind: "cmd-start", Payload: "vim notes"})
	p.output([]byte("\x1b[?1049h\x1b[22;0;0t\x1b[H\x1b[2Jnotes\r\n~\r\n"))
	p.output([]byte("\x1b[?1049l\x1b[23;0;0t"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/home/u"})

	es := p.sess.Entries()
	if len(es) != 2 {
		t.Fatalf("journal %+v", es)
	}
	want := "Welcome to remote\n" +
		"user@remote:~$ ls\na.txt\n" +
		"user@remote:~$ vim a.txt\n" + capture.FullScreen + "\n" +
		"user@remote:~$ cat a.txt\nline one\n" +
		"user@remote:~$ exit\nlogout\nConnection to remote closed."
	if e := es[0]; e.Kind != session.KindShell || e.Output != want || e.TUI {
		t.Errorf("ssh recorded as %q, TUI %v; want %q", e.Output, e.TUI, want)
	}
	if e := es[1]; e.Output != capture.FullScreen || !e.TUI {
		t.Errorf("vim recorded as %q, TUI %v", e.Output, e.TUI)
	}
}

// The agent's commands go by the same rule.
func TestRecorderAgentFullScreen(t *testing.T) {
	p, _ := recorderProxy(t, t.TempDir())
	p.marker(Marker{Kind: "ask-start"})
	for _, c := range []struct {
		id, cmd, out, want string
		tui                bool
	}{
		{"c1", "git log", "\x1b[?1049h\x1b[Hcommit 1\r\n:\x1b[?1049l", capture.FullScreen, true},
		{"c2", "make menu", "building\r\n\x1b[?1049h\x1b[Hmenu", "building\n" + capture.FullScreen, false},
	} {
		p.marker(Marker{Kind: "agent-start", Payload: c.id + ";" + c.cmd})
		p.output([]byte(c.out))
		p.marker(Marker{Kind: "agent-end", Payload: c.id + ";0;/tmp"})
		got, err := p.wait(context.Background(), c.id, time.Millisecond)
		if err != nil || got.Output != c.want || got.TUI != c.tui {
			t.Errorf("%s: %+v, %v; want %q, TUI %v", c.cmd, got, err, c.want, c.tui)
		}
	}
}
