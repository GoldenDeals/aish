package proxy

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/session"
)

// What bash 5.3 prints (PS1='$ ') for a line typed ahead while `clear`
// ran: the prompt with the line, the line again as the Enter's bind -x
// redraws it, and accept-line's new line. No key comes at this prompt.
const (
	aheadTitle  = "\x1b]0;user@host:~\a"
	aheadPrompt = aheadTitle + "\x1b[?2004h$ echo x\r\x1b[K\r$ echo x\r\n\x1b[?2004l\r"
	// A request typed ahead: Enter's route rewrote the line, and
	// __aish_unecho draws the request over its echo.
	aheadAsk = aheadTitle + "\x1b[?2004h$ Hello there\r\x1b[K\r$ __aish_ask \"$__aish_req\"\r\n\x1b[?2004l\r" +
		"\x1b[1A\r\x1b[K\x1b[B\x1b[J\x1b[A? Hello there\r\n"
)

// aheadProxy is a proxy at the prompt after `clear`, with out what the
// terminal got since the screen was erased.
func aheadProxy(t *testing.T) (p *Proxy, out *terminal, from int, dir string) {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p = New(sess)
	out = &terminal{}
	p.out, p.promptStatus, p.model = out, true, "test-model"
	p.size = func() (int, int) { return 80, 24 }
	dir = t.TempDir()
	p.marker(Marker{Kind: "cmd-start", Payload: "clear"})
	p.output([]byte("\x1b[H\x1b[2J\x1b[3J"))
	from = len(out.String())
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	if !strings.Contains(out.String()[from:], "test-model") {
		t.Fatalf("no status at the prompt after clear: %q", out.String()[from:])
	}
	return p, out, from, dir
}

// A line typed ahead is accepted with no key at its prompt. The new line
// that ends it takes the status down, as a prompt's own new line would;
// when the command starts, the status is off that line, where the
// command's output goes.
func TestPromptStatusTypedAhead(t *testing.T) {
	p, out, from, dir := aheadProxy(t)
	p.output([]byte(aheadPrompt))
	p.marker(Marker{Kind: "cmd-start", Payload: "echo x"})
	if p.line != nil {
		t.Error("cmd-start: the line is still followed")
	}
	p.output([]byte("x\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
	p.output([]byte("$ "))

	lines := strings.Split(capture.CleanScreen([]byte(out.String()[from:]), 80), "\n")
	if len(lines) != 3 || lines[0] != "$ echo x" || lines[1] != "x" ||
		!strings.HasPrefix(lines[2], "$ ") || !strings.HasSuffix(lines[2], "test-model") {
		t.Errorf("screen:\n%s\nwant the status at the last prompt alone", strings.Join(lines, "\n"))
	}
}

// The same for a request typed ahead: the agent's reply begins on a line
// with no status.
func TestPromptStatusRequestTypedAhead(t *testing.T) {
	p, out, from, _ := aheadProxy(t)
	p.output([]byte(aheadAsk))
	p.marker(Marker{Kind: "ask-start"})
	if p.line != nil {
		t.Error("ask-start: the line is still followed")
	}
	if got := capture.CleanScreen([]byte(out.String()[from:]), 80); got != "? Hello there" {
		t.Errorf("screen:\n%s\nwant the request alone", got)
	}
}

// The status leaves with the prompt only when a new line took it down
// before any key. A key keeps it on the prompt's line, above the output.
func TestInputLineLeave(t *testing.T) {
	l := newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	feedSteps(t, "typed ahead", l, []step{{"$ ls\r\n", "$ ls\r" + lineHide + "\n" + lineShow}})
	if got := string(l.leave()); got != lineHide {
		t.Errorf("leave %q, want %q", got, lineHide)
	}
	if got := string(l.leave()); got != "" {
		t.Errorf("leave again %q", got)
	}

	// Nothing printed: the line is still the prompt's.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	if got := string(l.leave()); got != "" {
		t.Errorf("leave with nothing printed %q", got)
	}

	// The prompt's own new line took the status down to its last line, and
	// a key keeps it there: the new line after the key leaves it above.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	l.feed([]byte("top\r\n$ "))
	l.typed()
	l.feed([]byte("\r\n"))
	if got := string(l.leave()); got != "" {
		t.Errorf("leave after a key %q", got)
	}

	// Gone with the first key's text, the status has nothing to erase.
	l = atPrompt(t)
	l.feed([]byte("ls\r\n"))
	if got := string(l.leave()); got != "" {
		t.Errorf("leave after typing %q", got)
	}

	// Inside a string the shell left open, ESC would end it.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	l.feed([]byte("$ ls\r\n\x1b]0;ti"))
	if got := string(l.leave()); got != "" {
		t.Errorf("leave inside OSC %q", got)
	}
}
