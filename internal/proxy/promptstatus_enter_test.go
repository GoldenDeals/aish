package proxy

import (
	"testing"

	"github.com/inebotov/aish/internal/session"
)

// Enter on an empty line takes the status off the prompt it was drawn for;
// Enter after text has nothing to erase, the status went with the first key.
func TestPromptStatusErasedOnEnter(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out, p.promptStatus, p.model = out, true, "m"
	p.size = func() (int, int) { return 80, 24 }
	dir := t.TempDir()
	const (
		draw  = "\x1b7\x1b[79G\x1b[2mm\x1b[0m\x1b8"
		erase = "\x1b7\x1b[79G\x1b[K\x1b8"
	)
	written := 0
	expect := func(what, want string) {
		t.Helper()
		s := out.String()
		if got := s[written:]; got != want {
			t.Errorf("%s: %q, want %q", what, got, want)
		}
		written = len(s)
	}

	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	p.output([]byte("$ "))
	expect("prompt", draw+"$ ")
	if got := p.key([]byte("\r")); string(got) != "\r" {
		t.Errorf("Enter went to the shell as %q", got)
	}
	expect("Enter on an empty line", erase)
	if got := p.key([]byte("\r")); string(got) != "\r" {
		t.Errorf("second Enter went to the shell as %q", got)
	}
	expect("Enter again", "")
	p.output([]byte("\r\n"))
	written = len(out.String())

	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	expect("next prompt", draw)
	p.output([]byte("$ "))
	p.key([]byte("a"))
	p.output([]byte("a"))
	expect("typed", "$ a"+erase)
	p.key([]byte("\r"))
	expect("Enter after text", "")
}
