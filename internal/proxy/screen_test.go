package proxy

import (
	"io"
	"testing"

	"github.com/inebotov/aish/internal/session"
)

// tmux and screen clear the screen with `\e[H\e[J`, not `\e[2J`.
func TestScreenHomeErase(t *testing.T) {
	type feed struct {
		chunk   string
		cleared bool
		from    int // checked only when cleared
	}
	cases := []struct {
		name  string
		feeds []feed
	}{
		{"Ctrl+L", []feed{{"\x1b[H\x1b[J$ ", true, 6}}},
		{"split after home", []feed{{"\x1b[H", false, 0}, {"\x1b[J$ ", true, 3}}},
		{"split inside erase", []feed{{"\x1b[H\x1b[", false, 0}, {"J", true, 1}}},
		{"clear under tmux", []feed{{"\x1b[H\x1b[J\x1b[3J", true, 10}}},
		{"clear under screen", []feed{{"clear\r\n\x1b[?2004l\r\x1b[H\x1b[J\x1b[?2004h$ ", true, 22}}},
		{"alternate screen", []feed{{"\x1b[?1049h\x1b[H\x1b[J", false, 0}}},
		{"erase below cursor", []feed{{"\x1b[J", false, 0}}},
		{"home then text", []feed{{"\x1b[Hls", false, 0}}},
		{"home at the end", []feed{{"\x1b[H", false, 0}, {"ls", false, 0}}},
	}
	for _, c := range cases {
		var s Screen
		for j, f := range c.feeds {
			cleared, from := s.Feed([]byte(f.chunk))
			if cleared != f.cleared || cleared && from != f.from {
				t.Errorf("%s, chunk %d %q: cleared=%v from=%d, want %v %d", c.name, j, f.chunk, cleared, from, f.cleared, f.from)
			}
		}
	}
}

func TestOutputHomeEraseClears(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = io.Discard
	sess.Append(session.Entry{Kind: session.KindShell, Cmd: "echo secret", Output: "secret\n"})
	p.output([]byte("\x1b[H\x1b[J$ "))
	es := sess.Entries()
	if len(es) != 2 || es[1].Kind != session.KindClear {
		t.Fatalf("after Ctrl+L under tmux: %+v", es)
	}
}
