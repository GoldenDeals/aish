package proxy

import (
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/session"
)

// bash runs a paste up to its syntax error: the commands before the error,
// and any line that may be one, are matched too.
func TestIgnoredCommandParseError(t *testing.T) {
	patterns := []string{"env"}
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"echo hi\nenv\necho 'oops", true},     // a command before the error
		{"env\necho 'oops", true},              // the first one
		{"sudo env\necho 'oops", true},         // matched the way the policy sees it
		{"echo hi\n  env  \necho 'oops", true}, // whatever the blanks around it
		{"echo 'oops\nenv", true},              // a line past the error, which bash would not run
		{"echo hi\necho 'oops", false},
		{"echo hi\necho env 'oops", false},
		{"echo 'oops\n\nprintenv", false},
	} {
		if got := ignoredCommand(tc.line, patterns); got != tc.want {
			t.Errorf("%q: ignored %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestJournalIgnoreParseError(t *testing.T) {
	sess, err := session.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = &terminal{}
	p.ignore = []string{"env"}
	line := "echo hi\nenv\necho 'oops"
	p.marker(Marker{Kind: "cmd-start", Payload: line})
	p.output([]byte("hi\r\nFOO_TOKEN=x\r\nbash: unexpected EOF while looking for matching `''\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "2;/tmp"})
	es := sess.Entries()
	if len(es) != 1 || es[0].Cmd != line || es[0].Output != session.NotRecorded || es[0].Exit != 2 {
		t.Fatalf("journal: %+v", es)
	}
}
