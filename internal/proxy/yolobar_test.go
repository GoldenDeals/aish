package proxy

import (
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// viewerBar is the status bar of the viewer's last frame in s: what its
// last row leaves, the bracketed paste mode the keys are read in after it
// cut.
func viewerBar(t *testing.T, s string) string {
	t.Helper()
	i := strings.LastIndex(s, "\x1b[H")
	if i < 0 {
		t.Fatalf("no frame of the viewer: %q", s)
	}
	s = s[i:]
	s = s[strings.LastIndex(s, "\r\n")+2:]
	if j := strings.Index(s, "\x1b[?2004"); j >= 0 {
		s = s[:j]
	}
	return s
}

// lastPanes is the screen the last frame of the panes in s leaves.
func lastPanes(t *testing.T, s string, w, h int) [][]rune {
	t.Helper()
	i := strings.LastIndex(s, "\x1b[?7l")
	if i < 0 {
		t.Fatalf("no frame of the panes: %q", s)
	}
	return screenOf(t, []byte(s[i:]), w, h)
}

// While yolo is on, the bar of the Ctrl+O viewer ends with its mark at the
// right edge, the keys cut for it; aish yolo off takes it off the open
// viewer at once. Without yolo there is none, and the bar is cut to the
// screen as well.
func TestYoloViewerBar(t *testing.T) {
	p, out, _ := hosted(t, &scripted{})
	const w = 40
	p.size = func() (int, int) { return w, 24 }
	p.folds = []Fold{{Title: "❯ ls", Text: "a\nb\n"}}

	openViewer(t, p)
	bar := viewerBar(t, out.String())
	if strings.Contains(bar, yoloMark) {
		t.Errorf("the mark without yolo: %q", bar)
	}
	if !strings.HasPrefix(bar, "\x1b[7m 1-") || runewidth.StringWidth(stripCSI(bar)) > w {
		t.Errorf("bar without yolo %q", bar)
	}
	p.key([]byte("q"))

	yoloByUser(t, p)
	openViewer(t, p)
	bar = viewerBar(t, out.String())
	mark := fmt.Sprintf("\x1b[0m\x1b[K\x1b[%dG %s%s%s", w-len(yoloMark), yoloColor, yoloMark, reset)
	text, ok := strings.CutSuffix(bar, mark)
	if !ok {
		t.Fatalf("no mark at the right edge: %q", bar)
	}
	if got := runewidth.StringWidth(stripCSI(text)); got != w-len(yoloMark)-1 {
		t.Errorf("the bar's text takes %d columns: %q", got, text)
	}

	if _, err := call(t, p, rpc.MethodYolo, rpc.YoloParams{}); err != nil || p.yoloOn() {
		t.Fatalf("off: %v, on %v", err, p.yoloOn())
	}
	if bar := viewerBar(t, out.String()); strings.Contains(bar, yoloMark) {
		t.Errorf("the mark stayed on the open viewer: %q", bar)
	}
	p.mu.Lock()
	open := p.view != nil
	p.mu.Unlock()
	if !open {
		t.Error("aish yolo off closed the viewer")
	}
}

// While yolo is on, the bar of the subagents' panes ends with its mark at
// the right edge, and so does the header of the pane zoomed, which has
// the keys then; aish yolo off takes it off the panes shown at once.
// Without yolo there is none.
func TestYoloPanesBar(t *testing.T) {
	const w, h = 80, 24
	for _, yolo := range []bool{false, true} {
		p, out, u, _ := paneProxy(t)
		p.yolo = yolo
		startPane(u, "one")
		startPane(u, "two")
		due(p)
		scr := lastPanes(t, out.String(), w, h)
		covered(t, scr)
		row := string(scr[h-1])
		if strings.HasSuffix(row, " "+yoloMark) != yolo {
			t.Errorf("yolo %v: bar %q", yolo, row)
		}
		if !strings.HasPrefix(row, " 2 running   1-2 zoom") {
			t.Errorf("yolo %v: bar %q", yolo, row)
		}

		p.key([]byte("1"))
		scr = lastPanes(t, out.String(), w, h)
		covered(t, scr)
		head := string(scr[0])
		if strings.HasSuffix(head, " "+yoloMark) != yolo || !strings.HasPrefix(head, " 1 one  running   0 grid") {
			t.Errorf("yolo %v: zoomed header %q", yolo, head)
		}
		if s := out.String(); strings.Contains(s, yoloColor+yoloMark) != yolo {
			t.Errorf("yolo %v: the mark's color %q", yolo, s)
		}
	}

	// aish yolo off once the request is over, the panes still shown: a
	// second Ctrl+C let the shell back to its prompt.
	p, out, u, _ := paneProxy(t)
	p.yolo = true
	startPane(u, "one")
	due(p)
	p.asking = false
	p.fg = func() (int, error) { return syscall.Getpgrp(), nil }
	if _, err := call(t, p, rpc.MethodYolo, rpc.YoloParams{}); err != nil || p.yoloOn() {
		t.Fatalf("off: %v, on %v", err, p.yoloOn())
	}
	scr := lastPanes(t, out.String(), w, h)
	if row := string(scr[h-1]); strings.Contains(row, yoloMark) {
		t.Errorf("the mark stayed on the panes: %q", row)
	}
}

// stripCSI is s without its CSI sequences.
func stripCSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
