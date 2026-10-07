package proxy

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/session"
)

// statusAfter runs a command that prints out, write by write, and reports
// whether cmd-end draws the status for the prompt that follows.
func statusAfter(t *testing.T, out ...string) bool {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	term := &terminal{}
	p.out, p.promptStatus, p.model = term, true, "m"
	p.size = func() (int, int) { return 80, 24 }
	p.marker(Marker{Kind: "cmd-start", Payload: "cmd"})
	for _, s := range out {
		p.output([]byte(s))
	}
	written := len(term.String())
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + t.TempDir()})
	drawn := strings.Contains(term.String()[written:], "\x1b7")
	if drawn != (p.line != nil) {
		t.Errorf("%q: status drawn %v, line followed %v", out, drawn, p.line != nil)
	}
	return drawn
}

// The prompt starts where the command's output left the cursor, and the
// input line's model counts columns from 0: after output that does not
// end its line, the status is not drawn, or its erasing would come too
// late for the text typed under it.
func TestPromptStatusFirstColumn(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  []string
		want bool
	}{
		{"no newline", []string{"abc"}, false},
		{"newline", []string{"abc\n"}, true},
		{"CRLF", []string{"abc\r\n"}, true},
		{"carriage return", []string{"abc\r"}, true},
		{"no output", nil, true},
		{"text on the next line", []string{"\r\nabc"}, false},
		{"tab", []string{"\r\n\t"}, false},
		{"wide", []string{"\r\né"}, false},

		// Sequences that leave the column alone.
		{"colors", []string{"abc\r\n\x1b[0m\x1b[m"}, true},
		{"modes", []string{"abc\r\n\x1b[?25h\x1b[?2004l\x1b=\x1b>"}, true},
		{"keyboard, cursor shape", []string{"abc\r\n\x1b[ q\x1b[<u\x1b[>4;m"}, true},
		{"up, erase", []string{"abc\r\n\x1b[A\x1b[K\x1b[J"}, true},
		{"left", []string{"\r\n\b\x1b[D"}, true},
		{"title", []string{"abc\r\n\x1b]0;user@host: ~\a"}, true},
		{"title, ST", []string{"abc\r\n\x1b]0;user@host: ~\x1b\\"}, true},
		{"title, then text", []string{"\x1b]0;t\aabc"}, false},

		// Cut short between writes.
		{"sequence cut", []string{"abc\r\n\x1b[", "0m"}, true},
		{"title cut", []string{"abc\r\n\x1b]0;us", "er@host\a"}, true},
		{"character cut", []string{"\xc3", "\xa9"}, false},

		// Sequences that move the cursor along the line.
		{"column 1", []string{"abc\x1b[G"}, true},
		{"column 5", []string{"\x1b[5G"}, false},
		{"home", []string{"abc\x1b[H"}, true},
		{"row 3, column 1", []string{"abc\x1b[3;1H"}, true},
		{"row 3, column 5", []string{"\x1b[3;5H"}, false},
		{"right", []string{"\r\n\x1b[2C"}, false},
		{"next line", []string{"abc\x1b[E"}, true},
		{"NEL", []string{"abc\x1bE"}, true},
		{"clear", []string{"abc\r\n\x1b[H\x1b[2J\x1b[3J"}, true},
		{"reset", []string{"abc\x1bc"}, true},
		{"restored after text", []string{"\x1b7abc\x1b8"}, true},
		{"restored to text", []string{"abc\x1b7\r\n\x1b8"}, false},
		{"restored, CSI", []string{"abc\x1b[s\r\n\x1b[u"}, false},

		// A full-screen program goes back to where it started.
		{"vim", []string{
			"\x1b[?1049h\x1b[22;0;0t\x1b[?1h\x1b=\x1b[H\x1b[2J~\x1b[24;5Hx",
			"\x1b[?1049l\x1b[23;0;0t\x1b[?1l\x1b>\x1b[?25h",
		}, true},
		{"vim after text", []string{"abc\x1b[?1049h\x1b[H\r\n", "\x1b[?1049l"}, false},
		{"alternate screen without saving", []string{"\x1b[?47h\x1b[5;5Hx\x1b[?47l"}, false},
		{"smcup with \\e7", []string{"\x1b7\x1b[?47h\x1b[5;5Hx", "\x1b[2J\x1b[?47l\x1b8"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusAfter(t, tc.out...); got != tc.want {
				t.Errorf("%q: status drawn %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// A request ends with the agent's own output, which ends its lines: the
// output of its commands, which the agent prints after, decides nothing.
func TestPromptStatusAfterRequest(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	term := &terminal{}
	p.out, p.promptStatus, p.model = term, true, "m"
	p.size = func() (int, int) { return 80, 24 }
	p.output([]byte("$ Hello\r\n"))
	p.marker(Marker{Kind: "ask-start"})
	p.output([]byte("abc"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + t.TempDir()})
	if p.line == nil {
		t.Error("no status after a request")
	}

	// The prompt itself and the line typed: the Enter that runs it ends it.
	p.output([]byte("$ printf abc"))
	p.marker(Marker{Kind: "cmd-start", Payload: "printf abc"})
	p.output([]byte("\r\nabc"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + t.TempDir()})
	if p.line != nil {
		t.Error("status after abc")
	}
	p.output([]byte("abc$ true"))
	p.marker(Marker{Kind: "cmd-start", Payload: "true"})
	p.output([]byte("\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + t.TempDir()})
	if p.line == nil {
		t.Error("no status after the next command")
	}
}
