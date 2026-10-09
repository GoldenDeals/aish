package proxy

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// lastFrame is the last frame the viewer drew in s, row by row, its bar
// last.
func lastFrame(s string) []string {
	i := strings.LastIndex(s, "\x1b[H")
	if i < 0 {
		return nil
	}
	return screenRows(s[i:])
}

// openViewer opens the viewer of p with Ctrl+O and returns it.
func openViewer(t *testing.T, p *Proxy) *viewer {
	t.Helper()
	if b := p.key([]byte{ctrlO}); len(b) != 0 {
		t.Fatalf("Ctrl+O went to the shell: %q", b)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.view == nil {
		t.Fatal("no viewer")
	}
	return p.view
}

// The open viewer shows the output of the running command as it comes,
// and the command once it ended, past what it holds for the screen; it
// draws nothing when nothing came, nor once closed.
func TestViewerLive(t *testing.T) {
	p, out := statusProxy(t, 40)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;seq 30"})
	p.output([]byte("1\r\n2\r\n"))
	v := openViewer(t, p)
	if rows := lastFrame(out.String()); !slices.Contains(rows, "❯ seq 30  (running)") || !slices.Contains(rows, "2") {
		t.Fatalf("opened on %q", rows)
	}

	p.output([]byte("3\r\n"))
	p.viewFrame(v)
	if rows := lastFrame(out.String()); !slices.Contains(rows, "3") || !slices.Contains(rows, "❯ seq 30  (running)") {
		t.Errorf("after output: %q", rows)
	}
	p.mu.Lock()
	held := string(p.held)
	p.mu.Unlock()
	if strings.Contains(held, "\x1b[H") {
		t.Errorf("a frame held for the screen: %q", held)
	}

	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	p.viewFrame(v)
	rows := lastFrame(out.String())
	if !slices.Contains(rows, "❯ seq 30") || !slices.Contains(rows, "3") {
		t.Errorf("after the end: %q", rows)
	}
	for _, r := range rows {
		if strings.Contains(r, "(running)") {
			t.Errorf("still running: %q", rows)
		}
	}

	before := out.String()
	p.viewFrame(v)
	if got := strings.TrimPrefix(out.String(), before); got != "" {
		t.Errorf("drew with nothing new: %q", got)
	}

	p.key([]byte("q"))
	p.mu.Lock()
	p.folds = append(p.folds, Fold{Title: "❯ ls", Text: "a\n"})
	p.mu.Unlock()
	before = out.String()
	p.viewFrame(v)
	if got := strings.TrimPrefix(out.String(), before); got != "" {
		t.Errorf("drew once closed: %q", got)
	}
}

// The viewer looks for new output by itself, every viewTick.
func TestViewerTicks(t *testing.T) {
	p, out := statusProxy(t, 40)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;seq 30"})
	p.output([]byte("1\r\n"))
	openViewer(t, p)
	defer p.key([]byte("q"))
	p.output([]byte("2\r\n"))
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(lastFrame(out.String()), "2"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no frame with the new line: %q", lastFrame(out.String()))
		}
	}
}

// numbered is the output of seq n.
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String()
}

// The viewer follows the end of the folds while the user has not scrolled
// it, or has scrolled back to the end; scrolled up, it stays where it is.
func TestViewerFollow(t *testing.T) {
	seq := func(n int) []Fold {
		return []Fold{{Title: "❯ ls", Text: "a\n"}, {Title: "❯ seq", Text: numbered(n)}}
	}
	lastRow := func(v *viewer) string { return v.rows[v.top+v.page()-1] }

	// The page is 7 rows. While the folds fit, the top stays: the rows
	// come below.
	v := newViewer(seq(1), 20, 8)
	if v.top != 0 || !v.update(seq(2)) || v.top != 0 {
		t.Fatalf("fitting: top %d, rows %q", v.top, v.rows)
	}
	if !v.update(seq(10)) || !v.atEnd() || lastRow(v) != "10" {
		t.Fatalf("not scrolled: top %d, last row %q", v.top, lastRow(v))
	}
	if v.update(seq(10)) {
		t.Error("the same folds changed the viewer")
	}

	v.key([]byte("k"))
	top := v.top
	v.update(seq(20))
	if v.top != top {
		t.Errorf("scrolled up: top %d, was %d", v.top, top)
	}

	v.key([]byte("G"))
	v.update(seq(30))
	if !v.atEnd() || lastRow(v) != "30" {
		t.Errorf("back at the end: top %d, last row %q", v.top, lastRow(v))
	}

	// Scrolled down to the last row, by pages, it follows too.
	v.key([]byte("g"))
	v.key([]byte(" "))
	v.update(seq(31))
	if v.top != v.page() {
		t.Fatalf("a page down: top %d", v.top)
	}
	for !v.atEnd() {
		v.key([]byte(" "))
	}
	v.update(seq(40))
	if lastRow(v) != "40" {
		t.Errorf("paged to the end: last row %q", lastRow(v))
	}

	// A narrower terminal keeps the view at the end.
	v.resize(10, 6)
	if !v.atEnd() {
		t.Errorf("resized: top %d of %d rows", v.top, len(v.rows))
	}
}

// read_file ends a line's number with a tab, ls lines up its columns with
// tabs: the viewer puts them at the tab stops, as the terminal would, not
// eight spaces for each.
func TestViewerTabs(t *testing.T) {
	p, _ := statusProxy(t, 80)
	u := &ui{p: p}
	u.Hidden("⚙ read_file x.go", "     1\tpackage x\n     2\t\n     3\tfunc f() {\n     4\t\treturn\n    10\t}\n")
	u.Hidden("⚙ ls", "a\t\tbb\tc\n日本\tx\n")
	v := openViewer(t, p)
	want := []string{
		"⚙ read_file x.go",
		"     1  package x",
		"     2  ",
		"     3  func f() {",
		"     4          return",
		"    10  }",
		"",
		"⚙ ls",
		"a               bb      c",
		"日本    x",
	}
	if !slices.Equal(v.rows, want) {
		t.Errorf("rows\n%q, want\n%q", v.rows, want)
	}
	if v := newViewer([]Fold{{Title: "❯ printf 'a\\tb'\n\tc"}}, 80, 24); !slices.Equal(v.rows, []string{"❯ printf 'a\\tb'", "        c"}) {
		t.Errorf("title: %q", v.rows)
	}
}

// A line of wide characters wraps where the screen would: no row of the
// frame is wider than the terminal.
func TestViewerWide(t *testing.T) {
	text := "a" + strings.Repeat("日本語", 10)
	v := newViewer([]Fold{{Title: "❯ cat", Text: text + "\n"}}, 10, 24)
	want := []string{"❯ cat", "a日本語日", "本語日本語", "日本語日本", "語日本語日", "本語日本語", "日本語日本", "語"}
	if !slices.Equal(v.rows, want) {
		t.Errorf("rows\n%q, want\n%q", v.rows, want)
	}
	v = newViewer([]Fold{{Title: "❯ cat", Text: strings.Repeat("日本語", 30) + "\n"}}, 25, 24)
	if strings.Join(v.rows[1:], "") != strings.Repeat("日本語", 30) {
		t.Errorf("lost text: %q", v.rows)
	}
	for _, r := range v.rows {
		if runewidth.StringWidth(r) > 25 {
			t.Errorf("row %q wider than 25 columns", r)
		}
	}
}
