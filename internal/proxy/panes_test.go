package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

// screenOf plays a frame of the layout on a w×h screen and returns its
// rows. A column the frame never wrote is \x00: a frame must cover the
// whole screen, as nothing clears it in between.
func screenOf(t *testing.T, frame []byte, w, h int) [][]rune {
	t.Helper()
	scr := make([][]rune, h)
	for i := range scr {
		scr[i] = make([]rune, w)
	}
	row, col := 0, 0
	for i := 0; i < len(frame); {
		if frame[i] == 0x1b {
			if i+1 >= len(frame) || frame[i+1] != '[' {
				t.Fatalf("not a CSI sequence at %d: %q", i, frame[i:])
			}
			j := i + 2
			for j < len(frame) && (frame[j] < 0x40 || frame[j] > 0x7e) {
				j++
			}
			if frame[j] == 'H' {
				if _, err := fmt.Sscanf(string(frame[i+2:j]), "%d;%d", &row, &col); err != nil {
					t.Fatalf("cursor position %q: %v", frame[i:j+1], err)
				}
				row, col = row-1, col-1
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRune(frame[i:])
		i += size
		if r < 0x20 {
			t.Fatalf("control character %q in the frame", r)
		}
		rw := runewidth.RuneWidth(r)
		if row < 0 || row >= h || col < 0 || col+rw > w {
			t.Fatalf("%q written at row %d, column %d of a %dx%d screen", r, row, col, w, h)
		}
		scr[row][col] = r
		col += rw
	}
	return scr
}

// covered fails unless the frame wrote every column of the screen.
func covered(t *testing.T, scr [][]rune) {
	t.Helper()
	for i, r := range scr {
		if j := slices.Index(r, 0); j >= 0 {
			t.Errorf("row %d, column %d left as it was: %q", i, j, string(r))
		}
	}
}

// text is what the rows of c show, the trailing spaces cut.
func text(scr [][]rune, c cell) []string {
	var out []string
	for y := c.y; y < c.y+c.h; y++ {
		out = append(out, strings.TrimRight(string(scr[y][c.x:c.x+c.w]), " "))
	}
	return out
}

func newPanes(w, h int, outs ...string) *panes {
	ps := &panes{zoom: -1}
	ps.resize(w, h)
	for i, o := range outs {
		pn := &pane{title: fmt.Sprintf("p%d", i+1), buf: capture.NewBuffer(foldRawCap, foldRawCap), exit: -1}
		pn.write([]byte(o))
		ps.list = append(ps.list, pn)
	}
	return ps
}

// The grid is as square as the panes fit, the cells cover the screen but
// its last line, one cell to a column, and none overlap.
func TestPaneGrid(t *testing.T) {
	for _, tc := range []struct {
		n, w, h    int
		cols, rows int
		cells      []cell
	}{
		{1, 80, 24, 1, 1, []cell{{0, 0, 80, 23}}},
		{2, 80, 24, 2, 1, []cell{{0, 0, 40, 23}, {40, 0, 40, 23}}},
		{3, 80, 24, 2, 2, []cell{{0, 0, 40, 11}, {40, 0, 40, 11}, {0, 11, 80, 12}}},
		{5, 80, 24, 3, 2, []cell{{0, 0, 26, 11}, {26, 0, 26, 11}, {52, 0, 28, 11}, {0, 11, 26, 12}, {26, 11, 54, 12}}},
		// No column narrower than 20 while the screen is wider.
		{4, 50, 24, 2, 2, nil},
		{4, 30, 24, 1, 4, nil},
		{3, 10, 3, 1, 3, nil},
	} {
		cols, rows, cells := grid(tc.n, tc.w, tc.h)
		if cols != tc.cols || rows != tc.rows || len(cells) != tc.n {
			t.Errorf("%d panes at %dx%d: %dx%d, %d cells", tc.n, tc.w, tc.h, cols, rows, len(cells))
			continue
		}
		if tc.cells != nil && !slices.Equal(cells, tc.cells) {
			t.Errorf("%d panes at %dx%d: %v, want %v", tc.n, tc.w, tc.h, cells, tc.cells)
		}
		owner := make([][]int, tc.h-1)
		for y := range owner {
			owner[y] = make([]int, tc.w)
		}
		for i, c := range cells {
			if c.x < 0 || c.y < 0 || c.w < 0 || c.h < 0 || c.x+c.w > tc.w || c.y+c.h > tc.h-1 {
				t.Errorf("%d panes at %dx%d: cell %v off the grid", tc.n, tc.w, tc.h, c)
				continue
			}
			for y := c.y; y < c.y+c.h; y++ {
				for x := c.x; x < c.x+c.w; x++ {
					if owner[y][x] != 0 {
						t.Fatalf("%d panes at %dx%d: cells %d and %d overlap at %d,%d", tc.n, tc.w, tc.h, owner[y][x]-1, i, y, x)
					}
					owner[y][x] = i + 1
				}
			}
		}
		for y, r := range owner {
			if x := slices.Index(r, 0); x >= 0 {
				t.Errorf("%d panes at %dx%d: %d,%d in no cell", tc.n, tc.w, tc.h, y, x)
			}
		}
	}
}

// Each pane shows its header and the last lines of its own output, cut
// and padded to its cell; the last line of the screen is the status bar.
func TestPaneFrame(t *testing.T) {
	var a strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&a, "a%d\n", i)
	}
	long := strings.Repeat("c", 100)
	ps := newPanes(80, 24, a.String(), "b1\nb2", long+"\ncend")
	ps.list[1].title = strings.Repeat("t", 60)
	scr := screenOf(t, ps.render(), 80, 24)
	covered(t, scr)
	_, _, cells := grid(3, 80, 24)

	// The first column has a separator at its right.
	left := text(scr, cell{0, 0, 39, 11})
	if left[0] != " 1 p1  running" {
		t.Errorf("header %q", left[0])
	}
	for i, l := range left[1:] {
		if want := fmt.Sprintf("a%d", 21+i); l != want {
			t.Errorf("row %d of p1: %q, want %q", i+1, l, want)
		}
	}
	for y := range 11 {
		if r := scr[y][39]; r != []rune(paneSep)[0] {
			t.Errorf("row %d: %q between the panes", y, r)
		}
	}
	// A header longer than the cell is cut; nothing runs into the next cell.
	right := text(scr, cells[1])
	if want := " 2 " + strings.Repeat("t", 37); right[0] != want {
		t.Errorf("long header %q", right[0])
	}
	if !slices.Equal(right[1:4], []string{"b1", "b2", ""}) {
		t.Errorf("p2 %q", right)
	}
	for _, c := range cells[:2] {
		for _, l := range text(scr, c)[1:] {
			if strings.ContainsAny(l, "abc") && !strings.HasPrefix(l, string(rune('a'+c.x/40))) {
				t.Errorf("a line of another pane in %v: %q", c, l)
			}
		}
	}
	// The last pane takes the whole width; a long line wraps.
	bottom := text(scr, cells[2])
	if !slices.Equal(bottom[:4], []string{" 3 p3  running", long[:80], long[80:], "cend"}) {
		t.Errorf("p3 %q", bottom[:4])
	}
	if bar := strings.TrimRight(string(scr[23]), " "); bar != " 3 subagents   1-3 zoom  0 grid  q detach  ctrl+c stop" {
		t.Errorf("status bar %q", bar)
	}

	// Zoomed, the pane takes the whole screen, the keys in its header.
	ps.zoom = 0
	scr = screenOf(t, ps.render(), 80, 24)
	covered(t, scr)
	all := text(scr, cell{0, 0, 80, 24})
	if !strings.HasPrefix(all[0], " 1 p1  running") || !strings.Contains(all[0], "0 grid") {
		t.Errorf("zoomed header %q", all[0])
	}
	if all[1] != "a8" || all[23] != "a30" {
		t.Errorf("zoomed rows %q … %q", all[1], all[23])
	}
}

// What a pane is written is not lost: lines longer than the pane wrap,
// carriage returns are applied as the terminal applies them, and the whole
// output goes to the fold.
func TestPaneOutput(t *testing.T) {
	ps := newPanes(80, 24)
	pn := &pane{title: "p", buf: capture.NewBuffer(foldRawCap, foldRawCap), exit: -1}
	for _, s := range []string{"one\r\ntwo\nthr", "ee\n", "50%\r100%\n", "\tx\x1b[31m!\x1b[0m"} {
		pn.write([]byte(s))
	}
	ps.list = []*pane{pn}
	if got := pn.rows(80, 10); !slices.Equal(got, []string{"one", "two", "three", "100%", "        x!"}) {
		t.Errorf("rows %q", got)
	}
	if got := pn.rows(3, 100); !slices.Equal(got, []string{"one", "two", "thr", "ee", "100", "%", "   ", "   ", "  x", "!"}) {
		t.Errorf("narrow rows %q", got)
	}
	if got := pn.rows(3, 2); !slices.Equal(got, []string{"  x", "!"}) {
		t.Errorf("the last rows %q", got)
	}
	if got := pn.rows(2, 3); !slices.Equal(got, []string{"  ", "  ", "x!"}) {
		t.Errorf("rows of two columns %q", got)
	}
	// A wide rune does not fit a column of one: it takes a row of its own,
	// which the cell then cuts.
	if got := hardWrap("日本", 1); !slices.Equal(got, []string{"日", "本"}) {
		t.Errorf("wide runes %q", got)
	}
	ps.resize(3, 3)
	covered(t, screenOf(t, ps.render(), 3, 3))

	// The tail a pane keeps to draw itself is bounded, from a line's start.
	big := &pane{title: "big", buf: capture.NewBuffer(foldRawCap, foldRawCap), exit: -1}
	line := strings.Repeat("x", 99) + "\n"
	for range 3 * paneTail / len(line) {
		big.write([]byte(line))
	}
	if n := len(big.tail); n > 2*paneTail || n < paneTail/2 || big.tail[0] != 'x' {
		t.Errorf("tail of %d bytes starting with %q", n, big.tail[0])
	}
	if got := big.rows(100, 2); !slices.Equal(got, []string{line[:99], line[:99]}) {
		t.Errorf("tail rows %q", got)
	}
}

// The header tells the state: running, ok, interrupted, or the exit code.
func TestPaneState(t *testing.T) {
	ps := newPanes(80, 24, "x", "y", "z", "w")
	ps.list[1].exit, ps.list[1].done = 0, true
	ps.list[2].exit, ps.list[2].done = 2, true
	ps.list[3].exit, ps.list[3].done = 130, true
	scr := screenOf(t, ps.render(), 80, 24)
	_, _, cells := grid(4, 80, 24)
	for i, want := range []string{" 1 p1  running", " 2 p2  ok", " 3 p3  rc 2", " 4 p4  interrupted"} {
		c := cells[i]
		if got := strings.TrimRight(string(scr[c.y][c.x:c.x+c.w-1]), " "); got != want {
			t.Errorf("header %q, want %q", got, want)
		}
	}
}

// A proxy with a terminal of w×h, inside a request.
func paneProxy(t *testing.T) (*Proxy, *terminal, *ui, *[2]int) {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	size := &[2]int{80, 24}
	p.size = func() (int, int) { return size[0], size[1] }
	p.asking = true
	return p, out, &ui{p: p}, size
}

func (tm *terminal) reset() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.b.Reset()
}

// due is paneDelay over: the layout opens as its timer would open it.
func due(p *Proxy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.panes != nil {
		p.panesDue(p.panes)
	}
}

// panesShown tells whether the layout is on the screen, safe to ask while its
// timer may open it.
func panesShown(p *Proxy) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.panes != nil && p.panes.shown
}

// While the layout is shown the keys are its own: a digit zooms, 0 and Esc
// go back to the grid, q and Ctrl+O leave it; Ctrl+C goes on to the shell.
// Ctrl+O brings the layout back, not the viewer of the folds.
func TestPaneKeys(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	p.folds = []Fold{{Title: "❯ ls", Text: "a\nb\n"}}
	for _, name := range []string{"one", "two", "three"} {
		u.Pane(name)
	}
	due(p)
	ps := p.panes
	if !ps.shown || !strings.HasPrefix(out.String(), panesOpen) {
		t.Fatalf("the layout did not open: %q", out.String())
	}
	for _, k := range []struct {
		in, pass string
		zoom     int
	}{
		{"3", "", 2},
		{"0", "", -1},
		{"2", "", 1},
		{"\x1b", "", -1},
		{"1\x1b[A", "", 0}, // an arrow is no Esc
		{"\x1bOq", "", 0},  // nor the keypad's 1
		{"9", "", 0},       // no such pane
		{"0\x03", "\x03", -1},
		{"\x03", "\x03", -1},
	} {
		got := p.key([]byte(k.in))
		settled(p) // a lone Esc, once nothing follows it
		if string(got) != k.pass || ps.zoom != k.zoom || !ps.shown {
			t.Errorf("%q: passed %q, zoom %d, shown %v", k.in, got, ps.zoom, ps.shown)
		}
	}
	p.key([]byte("3"))
	s := out.String()
	if f := s[strings.LastIndex(s, "\x1b[?7l"):]; !strings.Contains(f, " 3 three  running   0 grid") {
		t.Errorf("zoomed frame %q", f)
	}
	p.key([]byte("0"))

	for _, leave := range []string{"q", "\x0f"} {
		out.reset()
		if got := p.key([]byte(leave)); len(got) != 0 || ps.shown || modeless(out.String()) != panesClose {
			t.Errorf("%q: passed %q, shown %v, wrote %q", leave, got, ps.shown, out.String())
		}
		p.emit([]byte("text"))
		if !strings.HasSuffix(out.String(), "text") {
			t.Errorf("detached, the output is held: %q", out.String())
		}
		out.reset()
		if got := p.key([]byte("x\x0fy")); string(got) != "x" || !ps.shown || p.view != nil || !strings.HasPrefix(out.String(), panesOpen) {
			t.Errorf("Ctrl+O: passed %q, shown %v, viewer %v", got, ps.shown, p.view != nil)
		}
	}

	// A user's command gets Ctrl+O as usual.
	p.key([]byte("q"))
	p.user = &segment{}
	if got := p.key([]byte{ctrlO}); string(got) != "\x0f" || ps.shown {
		t.Errorf("a user's command: passed %q, shown %v", got, ps.shown)
	}
}

// While the layout is shown, the agent's and the shell's output waits, and
// comes once the screen is back, before the summaries; the output of each
// pane goes to the folds, and Ctrl+O shows them.
func TestPaneClose(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	a, b := u.Pane("alpha"), u.Pane("beta")
	due(p)
	a.Write([]byte("found\nit\n"))
	b.Write([]byte("oops\n"))
	p.mu.Lock()
	p.emit([]byte("held"))
	p.mu.Unlock()
	if strings.Contains(out.String(), "held") {
		t.Fatal("the output went over the layout")
	}
	a.Finish(0)
	if p.panes == nil || !strings.Contains(out.String(), " 1 alpha  ok") {
		t.Fatalf("one pane done: %q", out.String())
	}
	b.Finish(1)
	u.ClosePanes()
	b.Write([]byte("late"))
	b.Finish(0)
	if p.panes != nil || p.held != nil {
		t.Fatal("the layout stayed")
	}
	s := modeless(out.String())
	i := strings.LastIndex(s, panesClose)
	if i < 0 {
		t.Fatalf("the screen did not come back: %q", s)
	}
	want := "held" +
		"  " + paneOK + "✓" + reset + " alpha  " + dim + "(2 lines · ctrl+o to expand)" + reset + "\r\n" +
		"  " + paneFail + "✗" + reset + " beta  " + dim + "(1 line · rc 1 · ctrl+o to expand)" + reset + "\r\n"
	if got := s[i+len(panesClose):]; got != want {
		t.Errorf("after the layout\n got %q\nwant %q", got, want)
	}
	if want := []Fold{{Title: "alpha", Text: "found\nit"}, {Title: "beta", Text: "oops"}}; !slices.Equal(p.folds, want) {
		t.Errorf("folds %q", p.folds)
	}
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Error("Ctrl+O does not show the folds")
	}
}

// The layout waits while the viewer has the screen, and Ctrl+O brings it
// once the viewer is closed.
func TestPaneBehindViewer(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	p.folds = []Fold{{Title: "❯ ls", Text: "a\nb\n"}}
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	out.reset()
	w := u.Pane("alpha")
	w.Write([]byte("x\n"))
	due(p)
	if p.panes.shown || out.String() != "" {
		t.Fatalf("the layout took the viewer's screen: %q", out.String())
	}
	p.key([]byte("q"))
	if p.view != nil || p.panes.shown {
		t.Fatal("q closes the viewer only")
	}
	p.key([]byte{ctrlO})
	if !p.panes.shown || p.view != nil {
		t.Fatal("Ctrl+O does not bring the layout")
	}
	p.key([]byte{ctrlO})
	p.key([]byte{ctrlO})
	p.key([]byte("q"))

	// Detached, the end is as usual, minus the screen.
	out.reset()
	w.Finish(0)
	u.ClosePanes()
	if s := out.String(); strings.Contains(s, "\x1b[?1049") || !strings.Contains(s, "✓"+reset+" alpha") {
		t.Errorf("closed detached: %q", s)
	}
}

// A redraw comes at most once a paneRedraw, and what came meanwhile is
// drawn once the time is over.
func TestPaneRedraw(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	w := u.Pane("alpha")
	due(p)
	defer u.ClosePanes()
	defer w.Finish(0)
	p.mu.Lock()
	p.panes.last = time.Now().Add(300 * time.Millisecond)
	p.mu.Unlock()
	out.reset()
	w.Write([]byte("late\n"))
	p.mu.Lock()
	pending := p.panes.timer != nil
	p.mu.Unlock()
	if s := out.String(); s != "" || !pending {
		t.Fatalf("drawn at once: %q, timer %v", s, pending)
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), "late"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never drawn")
		}
	}
}

// A resize lays the panes out anew, down to a screen too small for them.
func TestPaneResize(t *testing.T) {
	ps := newPanes(80, 24, "a\n", "b\n", "c\n")
	for _, sz := range [][2]int{{10, 3}, {1, 1}, {3, 10}, {200, 60}} {
		ps.resize(sz[0], sz[1])
		for _, z := range []int{-1, 1} {
			ps.zoom = z
			covered(t, screenOf(t, ps.render(), sz[0], sz[1]))
		}
	}

	p, out, u, size := paneProxy(t)
	u.Pane("alpha")
	u.Pane("beta")
	due(p)
	out.reset()
	*size = [2]int{50, 10}
	p.resized()
	covered(t, screenOf(t, []byte(out.String()), 50, 10))
}

// Without a terminal nothing is drawn; the outputs are summed up and kept.
func TestPaneNoTerminal(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	p.size = nil
	w := u.Pane("alpha")
	w.Write([]byte("x\n"))
	due(p)
	if p.panes.shown {
		t.Fatal("shown without a terminal")
	}
	w.Finish(0)
	u.ClosePanes()
	if s := out.String(); s != "  "+paneOK+"✓"+reset+" alpha  "+dim+"(1 line · ctrl+o to expand)"+reset+"\r\n" {
		t.Errorf("terminal %q", s)
	}
	if len(p.folds) != 1 {
		t.Errorf("folds %q", p.folds)
	}
}

// Once the request is over, the prompt is on the screen: no summary goes
// there. The shell gone, the screen comes back all the same.
func TestPaneAfterRequest(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	w := u.Pane("alpha")
	due(p)
	w.Write([]byte("x\n"))
	p.asking = false
	w.Finish(130)
	u.ClosePanes()
	if s := modeless(out.String()); !strings.HasSuffix(s, panesClose) || len(p.folds) != 1 {
		t.Errorf("terminal %q, folds %q", s, p.folds)
	}

	p, out, u, _ = paneProxy(t)
	w = u.Pane("alpha")
	due(p)
	p.restoreScreen()
	if s := out.String(); !strings.Contains(s, panesClose) || p.panes != nil {
		t.Errorf("restored: %q", s)
	}
	before := out.String()
	w.Write([]byte("x\n"))
	w.Finish(0)
	u.ClosePanes()
	if s := out.String(); s != before {
		t.Errorf("written once the screen was restored: %q", s[len(before):])
	}
}

// The subagents past maxParallel start as the first ones end: the layout
// stays between them, all done for a moment, and goes once the call ends
// it, with one summary block for all of them.
func TestPanesStayForTheCall(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	var ws []agent.Live
	for i := range 4 {
		ws = append(ws, u.Pane(fmt.Sprintf("s%d", i+1)))
	}
	due(p)
	for _, w := range ws {
		w.Finish(0)
	}
	if !panesShown(p) || strings.Contains(out.String(), panesClose) || strings.Contains(out.String(), "✓") {
		t.Fatalf("the layout closed before the fifth: %q", out.String())
	}
	w := u.Pane("s5")
	w.Write([]byte("x\n"))
	w.Finish(0)
	if !panesShown(p) || strings.Contains(out.String(), "✓") {
		t.Fatalf("the layout closed with the fifth: %q", out.String())
	}
	u.ClosePanes()
	s := modeless(out.String())
	if strings.Count(s, panesOpen) != 1 || strings.Count(s, panesClose) != 1 {
		t.Fatalf("the layout opened %d times, closed %d", strings.Count(s, panesOpen), strings.Count(s, panesClose))
	}
	end := strings.Index(s, panesClose)
	if !strings.Contains(s[:end], " 5 s5  ok") {
		t.Errorf("the fifth is not in the layout: %q", s[:end])
	}
	var want strings.Builder
	for i := range 5 {
		what := "no output"
		if i == 4 {
			what = "1 line · ctrl+o to expand"
		}
		fmt.Fprintf(&want, "  %s✓%s s%d  %s(%s)%s\r\n", paneOK, reset, i+1, dim, what, reset)
	}
	if got := s[end+len(panesClose):]; got != want.String() {
		t.Errorf("after the layout\n got %q\nwant %q", got, want.String())
	}
	if p.panes != nil || panesShown(p) {
		t.Error("the layout stayed")
	}
}

// A subagent done before paneDelay leaves its summary without the
// alternate screen: the layout would only flash. Nor does it open later.
func TestPaneShortSubagent(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	w := u.Pane("alpha")
	w.Write([]byte("x\n"))
	time.Sleep(10 * time.Millisecond)
	w.Finish(0)
	u.ClosePanes()
	time.Sleep(paneDelay + 50*time.Millisecond)
	want := "  " + paneOK + "✓" + reset + " alpha  " + dim + "(1 line · ctrl+o to expand)" + reset + "\r\n"
	if s := out.String(); s != want {
		t.Errorf("terminal %q, want %q", s, want)
	}
	if p.panes != nil || len(p.folds) != 1 || p.folds[0].Text != "x" {
		t.Errorf("panes %v, folds %q", p.panes != nil, p.folds)
	}
}

// The layout opens once the subagents have run paneDelay, or at once on
// Ctrl+O; left with q then, it does not come back by itself.
func TestPaneDelay(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	start := time.Now()
	w := u.Pane("alpha")
	if s := out.String(); s != "" {
		t.Fatalf("opened at once: %q", s)
	}
	for deadline := time.Now().Add(5 * time.Second); !panesShown(p); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never opened")
		}
	}
	if d := time.Since(start); d < paneDelay {
		t.Errorf("opened after %v", d)
	}
	if s := out.String(); !strings.HasPrefix(s, panesOpen) || !strings.Contains(s, " 1 alpha  running") {
		t.Errorf("terminal %q", s)
	}
	w.Finish(0)
	u.ClosePanes()

	p, out, u, _ = paneProxy(t)
	w = u.Pane("alpha")
	if got := p.key([]byte{ctrlO}); len(got) != 0 || !panesShown(p) || !strings.HasPrefix(out.String(), panesOpen) {
		t.Fatalf("Ctrl+O: passed %q, terminal %q", got, out.String())
	}
	p.key([]byte("q"))
	time.Sleep(paneDelay + 50*time.Millisecond)
	if panesShown(p) || strings.Count(out.String(), panesOpen) != 1 {
		t.Errorf("the delay brought the layout back: %q", out.String())
	}
	w.Finish(0)
	u.ClosePanes()
}

// Ctrl+C echoes ^C into the live output of the task call while the layout
// is shown: once the screen is back, the call's status says it was
// interrupted, on its own line, and the summaries go below it.
func TestPaneCallInterrupted(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	p.at = &statusAt{col: 20, cols: 80}
	live := u.Live("⚙ task alpha")
	w := u.Pane("alpha")
	due(p)
	p.output([]byte("^C"))
	w.Finish(130)
	u.ClosePanes()
	live.Finish(130)
	s := out.String()
	after := s[strings.LastIndex(s, panesClose)+len(panesClose):]
	status, summary := strings.Index(after, "(1 line · interrupted · ctrl+o to expand)"), strings.Index(after, "\r\n  "+paneFail+"✗")
	if status < 0 || summary < status || strings.Count(after, "interrupted · ctrl") != 1 {
		t.Errorf("after the layout %q", after)
	}
	if len(p.folds) != 1 || p.folds[0].Title != "⚙ task alpha" || p.tool != nil {
		t.Errorf("folds %q", p.folds)
	}
}

// With fold_lines the call shows the first lines of what came into it:
// the last of them is ended before the summaries.
func TestPaneCallShown(t *testing.T) {
	p, out, u, _ := paneProxy(t)
	p.foldLines = 3
	live := u.Live("⚙ task alpha")
	w := u.Pane("alpha")
	due(p)
	p.output([]byte("bg job"))
	w.Finish(0)
	u.ClosePanes()
	live.Finish(-1)
	s := modeless(out.String())
	after := s[strings.LastIndex(s, panesClose)+len(panesClose):]
	if want := "bg job\r\n  " + paneOK + "✓"; !strings.HasPrefix(after, want) {
		t.Errorf("after the layout %q, want %q…", after, want)
	}
}

// paneGated holds a request until gate lets it go.
type paneGated struct {
	*scripted
	gate func(llm.Request)
}

func (g paneGated) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	g.gate(req)
	return g.scripted.Complete(ctx, req, onText)
}

// scoutCall is a request whose agent hands "look" to the subagent scout,
// which finds two files. Its requests to the model go through gate.
func scoutCall(t *testing.T, gate func(*Proxy, llm.Request)) (*Proxy, *terminal) {
	t.Helper()
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "task", Args: json.RawMessage(`{"tasks":[{"agent":"scout","prompt":"look around"}]}`)}}},
		{Text: "found two files"},
		{Text: "all done"},
	}}
	p, out, cwd := hosted(t, prov)
	dir := filepath.Join(cwd, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	def := "---\nname: scout\ndescription: Looks around\n---\nLook around.\n"
	if err := os.WriteFile(filepath.Join(dir, "scout.md"), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	p.newProvider = func(config.Config) (llm.Provider, error) {
		return paneGated{prov, func(req llm.Request) { gate(p, req) }}, nil
	}
	p.size = func() (int, int) { return 80, 24 }
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "look", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(p.folds) != 1 || p.folds[0].Title != "scout" || !strings.Contains(p.folds[0].Text, "found two files") {
		t.Errorf("folds %q", p.folds)
	}
	if p.panes != nil || p.tool != nil {
		t.Error("the layout or the call's live output stayed")
	}
	if es := p.sess.Entries(); len(es) < 3 || !strings.Contains(es[2].Output, "found two files") {
		t.Errorf("journal %+v", es)
	}
	return p, out
}

// The task call of a request: its subagent has a pane, and once it is done
// a summary goes below the call, whose line ends without a status of its
// own; the subagent's output is in the folds.
func TestPanesOfTaskCall(t *testing.T) {
	_, out := scoutCall(t, func(p *Proxy, req llm.Request) {
		if !strings.Contains(req.System, "Look around.") {
			return
		}
		// The subagent works until the layout is there.
		for deadline := time.Now().Add(5 * time.Second); !panesShown(p); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("the layout never opened")
				return
			}
		}
	})
	s := modeless(out.String())
	open, end := strings.Index(s, panesOpen), strings.Index(s, panesClose)
	if open < 0 || end < open {
		t.Fatalf("no layout: %q", s)
	}
	if !strings.Contains(s[:open], "task scout") {
		t.Errorf("the call is not before the layout: %q", s[:open])
	}
	if !strings.Contains(s[open:end], " 1 scout  running") {
		t.Errorf("the layout %q", s[open:end])
	}
	after := s[end+len(panesClose):]
	want := "\r\n  " + paneOK + "✓" + reset + " scout  " + dim + "(1 line · ctrl+o to expand)" + reset + "\r\n"
	if !strings.HasPrefix(after, want) || !strings.Contains(after, "all done") {
		t.Errorf("after the layout\n got %q\nwant %q…", after, want)
	}
	if strings.Contains(s, "no output") {
		t.Errorf("the call has a status of its own: %q", s)
	}
}

// A subagent done before paneDelay is over leaves its summary below the
// call as well, without the alternate screen.
func TestPanesOfQuickTaskCall(t *testing.T) {
	old := paneDelay
	paneDelay = time.Hour // however slow the machine
	t.Cleanup(func() { paneDelay = old })
	_, out := scoutCall(t, func(*Proxy, llm.Request) {})
	s := out.String()
	if strings.Contains(s, "\x1b[?1049") {
		t.Fatalf("the alternate screen: %q", s)
	}
	at := strings.Index(s, "task scout")
	want := "\r\n  " + paneOK + "✓" + reset + " scout  " + dim + "(1 line · ctrl+o to expand)" + reset + "\r\n"
	sum := strings.Index(s, want)
	if at < 0 || sum < at || !strings.Contains(s[sum:], "all done") || strings.Contains(s, "no output") {
		t.Errorf("terminal %q", s)
	}
}
