package proxy

import (
	"testing"
	"time"

	"github.com/inebotov/aish/internal/session"
)

// TestEarlyKeys: keys typed before the shell's first prompt, a bracketed
// paste among them, are held while the PTY would echo them and go to
// readline once it has the terminal, in the order typed. A key that sends
// a signal, or ~/.bashrc printing something, lets them go at once; a
// readline without bracketed paste gets them a while after the prompt.
func TestEarlyKeys(t *testing.T) {
	start := func(t *testing.T) (*Proxy, *terminal) {
		t.Helper()
		sess, err := session.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		p := New(sess)
		p.out = &terminal{}
		pty := &terminal{}
		p.holdEarly(pty)
		return p, pty
	}
	key := func(t *testing.T, p *Proxy, k, want string) {
		t.Helper()
		if got := p.key([]byte(k)); string(got) != want {
			t.Fatalf("key %q goes to the shell as %q, want %q", k, got, want)
		}
	}
	pty := func(t *testing.T, pty *terminal, what, want string) {
		t.Helper()
		if got := pty.String(); got != want {
			t.Fatalf("%s: the PTY got %q, want %q", what, got, want)
		}
	}

	t.Run("readline", func(t *testing.T) {
		p, in := start(t)
		p.promptStatus, p.model = true, "m"
		p.size = func() (int, int) { return 80, 24 }
		key(t, p, "\x1b[200~Привет! \x1b[201~", "")
		key(t, p, "\r", "")
		p.marker(Marker{Kind: "cmd-end", Payload: "0;/"})
		// PROMPT_COMMAND's title, then readline's sequence cut in two, as
		// Filter passes an ESC that starts no marker of ours.
		p.output([]byte("\x1b]0;title\a\x1b"))
		pty(t, in, "before readline", "")
		p.output([]byte("[?2004h$ "))
		pty(t, in, "readline has the terminal", "\x1b[200~Привет! \x1b[201~\r")
		if p.line == nil || !p.line.keyed {
			t.Error("the prompt's status takes no key typed at the prompt")
		}
		key(t, p, "x", "x")
	})

	t.Run("signal", func(t *testing.T) {
		p, in := start(t)
		key(t, p, "sleep", "")
		key(t, p, "\x03", "sleep\x03")
		key(t, p, "x", "x")
		pty(t, in, "after Ctrl+C", "")
	})

	t.Run("bashrc asks", func(t *testing.T) {
		p, in := start(t)
		key(t, p, "y\r", "")
		p.output([]byte("Passphrase: "))
		pty(t, in, "~/.bashrc printed", "y\r")
		key(t, p, "x", "x")
	})

	t.Run("no bracketed paste", func(t *testing.T) {
		defer func(w time.Duration) { earlyWait = w }(earlyWait)
		earlyWait = 10 * time.Millisecond
		p, in := start(t)
		key(t, p, "ls", "")
		p.marker(Marker{Kind: "cmd-end", Payload: "0;/"})
		p.output([]byte("$ "))
		for deadline := time.Now().Add(5 * time.Second); in.String() == "" && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		pty(t, in, "after the wait", "ls")
		p.output([]byte("\x1b[?2004h"))
		pty(t, in, "bracketed paste on later", "ls")
	})
}
