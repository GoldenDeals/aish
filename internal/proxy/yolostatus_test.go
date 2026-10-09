package proxy

import (
	"testing"

	"github.com/GoldenDeals/aish/internal/session"
)

// While yolo is on its mark is at the prompt with prompt_status off and in
// a terminal too narrow for the whole status, alone, as long as 40 columns
// are left past it; with yolo off neither draws anything.
func TestYoloMarkAlone(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	cols := 80
	p.out, p.model = out, "claude-opus-5 · a long model name"
	p.size = func() (int, int) { return cols, 24 }
	dir := t.TempDir()
	mark := func(col string) string { return "\x1b7\x1b[" + col + "G" + yoloColor + yoloMark + "\x1b[0m\x1b8" }
	written := 0
	prompt := func(what, want string) {
		t.Helper()
		p.marker(Marker{Kind: "cmd-end", Payload: "0;" + dir})
		s := out.String()
		if got := s[written:]; got != want {
			t.Errorf("%s: %q, want %q", what, got, want)
		}
		if (p.line != nil) != (want != "") {
			t.Errorf("%s: line followed %v", what, p.line != nil)
		}
		written = len(s)
	}

	for _, tc := range []struct {
		what   string
		status bool
		cols   int
		yolo   bool
		want   string
	}{
		{"prompt_status off", false, 80, false, ""},
		{"prompt_status off, yolo", false, 80, true, mark("76")},
		{"narrow", true, 60, false, ""},
		{"narrow, yolo", true, 60, true, mark("56")},
		{"44 columns, yolo", true, 44, true, mark("40")},
		{"43 columns, yolo", true, 43, true, ""},
		{"prompt_status off, 43 columns, yolo", false, 43, true, ""},
	} {
		p.promptStatus, cols, p.yolo = tc.status, tc.cols, tc.yolo
		prompt(tc.what, tc.want)
	}

	// The whole status where it fits, the mark red at its end.
	p.promptStatus, cols, p.yolo = true, 120, true
	prompt("wide, yolo", "\x1b7\x1b[80G\x1b[2mclaude-opus-5 · a long model name · "+yoloColor+yoloMark+"\x1b[0m\x1b8")

	// The mark alone hides from the line typed, as the status does.
	p.promptStatus, cols = false, 80
	prompt("prompt_status off, yolo, again", mark("76"))
	p.output([]byte("$ "))
	p.key([]byte("a"))
	p.output([]byte("a"))
	if got, want := out.String()[written:], "$ a\x1b7\x1b[76G\x1b[K\x1b8"; got != want {
		t.Errorf("typed: %q, want %q", got, want)
	}
}
