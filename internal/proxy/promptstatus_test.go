package proxy

import (
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/session"
)

// The proxy keeps the prompt's status off the line typed: hidden by the
// first key's echo, back when the line is empty, left alone once the
// command runs or the terminal changes its size.
func TestPromptStatusHides(t *testing.T) {
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
		draw   = "\x1b7\x1b[79G\x1b[2mm\x1b[0m\x1b8"
		erase  = "\x1b7\x1b[79G\x1b[K\x1b8"
		drawLs = "\x1b7\x1b[74G\x1b[2m11 · m\x1b[0m\x1b8" // with ls in the journal
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
	expect("cmd-end", draw)
	p.output([]byte("$ "))
	expect("prompt", "$ ")
	if got := p.key([]byte("a")); string(got) != "a" {
		t.Errorf("key %q", got)
	}
	p.output([]byte("a"))
	expect("echo", "a"+erase)
	p.output([]byte("\b\x1b[K"))
	expect("Backspace", "\b\x1b[K"+draw)
	p.output([]byte("ls"))
	expect("command", "ls"+erase)

	p.marker(Marker{Kind: "cmd-start", Payload: "ls"})
	if p.line != nil {
		t.Error("cmd-start: the line is still followed")
	}
	p.output([]byte("\r\nx\r\n"))
	expect("command output", "\r\nx\r\n")

	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	expect("cmd-end", drawLs)
	p.resized()
	if p.line != nil {
		t.Error("resized: the line is still followed")
	}
	p.output([]byte("x\r\n"))
	expect("after resizing", "x\r\n")

	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	expect("cmd-end", drawLs)
	p.marker(Marker{Kind: "ask-start"})
	if p.line != nil {
		t.Error("ask-start: the line is still followed")
	}

	// A sequence cut short when the line stops being followed still
	// reaches the terminal.
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	expect("cmd-end", drawLs)
	p.output([]byte("\x1b"))
	p.marker(Marker{Kind: "cmd-start", Payload: ""})
	p.output([]byte("[K"))
	expect("cut", "\x1b[K")

	p.promptStatus = false
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	if p.line != nil || strings.Contains(out.String()[written:], "\x1b7") {
		t.Errorf("prompt_status = false: line %v, terminal %q", p.line, out.String()[written:])
	}
}
