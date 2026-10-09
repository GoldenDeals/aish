package proxy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/session"
)

// A command the user typed goes to the journal as a terminal as wide as
// aish's shows it: an ssh session where a line longer than the terminal
// was edited and searched for with Ctrl+R is its prompts, lines and
// output, not the pieces of each redraw. Resized while it ran, the
// narrower width is the one: what was drawn at the wider one is not
// drawn over the lines above it.
func TestRecordUserTerminalWidth(t *testing.T) {
	raw, err := os.ReadFile("../capture/testdata/bash-40-columns.raw")
	if err != nil {
		t.Fatal(err)
	}
	want := capture.CleanScreen(raw, 40)
	if want == capture.Clean(raw) || strings.Contains(want, "reverse-i-search") {
		t.Fatalf("the recording reads the same without the width:\n%s", want)
	}
	for _, c := range []struct{ start, end int }{{40, 40}, {40, 80}, {80, 40}} {
		p, _ := recorderProxy(t, t.TempDir())
		cols := c.start
		p.size = func() (int, int) { return cols, 24 }
		p.marker(Marker{Kind: "cmd-start", Payload: "ssh remote"})
		for b := raw; len(b) > 0; {
			n := min(len(b), 64)
			p.output(b[:n])
			b = b[n:]
		}
		cols = c.end
		p.marker(Marker{Kind: "cmd-end", Payload: "0;/home/u"})
		es := p.sess.Entries()
		if len(es) != 1 || es[0].Kind != session.KindShell {
			t.Fatalf("%d to %d columns: journal %+v", c.start, c.end, es)
		}
		if got := es[0].Output; got != want {
			t.Errorf("%d to %d columns:\n%s\nwant:\n%s", c.start, c.end, got, want)
		}
	}
}

// The agent's commands go by the terminal's width too: a progress of
// several lines is its last frame.
func TestAgentCommandTerminalWidth(t *testing.T) {
	p, _ := recorderProxy(t, t.TempDir())
	p.size = func() (int, int) { return 40, 24 }
	p.marker(Marker{Kind: "ask-start"})
	p.marker(Marker{Kind: "agent-start", Payload: "c1;docker pull img"})
	p.output([]byte("Pulling img\r\na: 10%\r\nb: 20%\r\n"))
	p.output([]byte("\x1b[2A\x1b[2K\ra: done\r\n\x1b[2K\rb: done\r\nok\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	got, err := p.wait(context.Background(), "c1", time.Millisecond)
	if want := "Pulling img\na: done\nb: done\nok"; err != nil || got.Output != want {
		t.Errorf("%+v, %v; want %q", got, err, want)
	}
}
