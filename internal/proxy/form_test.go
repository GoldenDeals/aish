package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/session"
)

// twoQuestions are a question to choose one option of and one to check
// several of.
func twoQuestions() []agent.Question {
	return []agent.Question{
		{Question: "Which approach?", Header: "Approach", Options: []agent.Option{{Label: "Rewrite", Description: "Start from scratch"}, {Label: "Patch"}}},
		{Question: "Which features?", Header: "Features", MultiSelect: true, Options: []agent.Option{{Label: "Colors"}, {Label: "Icons"}, {Label: "Sounds"}}},
	}
}

const (
	keyUpSeq    = "\x1b[A"
	keyDownSeq  = "\x1b[B"
	keyLeftSeq  = "\x1b[D"
	keyEnterSeq = "\r"
	keyEraseSeq = "\x7f"
)

// Keys, each a read of its own, make the answers and the last frame.
func TestFormFeed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keys    []string
		want    []agent.Answer // nil: cancelled
		summary string
	}{
		{
			"arrows and space",
			[]string{keyDownSeq, keyEnterSeq, "1", keyDownSeq, keyDownSeq, " ", keyEnterSeq},
			[]agent.Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Colors", "Sounds"}}},
			"  Approach: Patch\n  Features: Colors, Sounds",
		},
		{
			// Back from the second question keeps what was typed in the first.
			"other and back",
			[]string{"3", "my wax", keyEraseSeq, "y", keyEnterSeq, keyEraseSeq, keyUpSeq, keyEnterSeq, "4", "lights", keyUpSeq, " ", keyEnterSeq},
			[]agent.Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Sounds"}, Other: "lights"}},
			"  Approach: Patch\n  Features: Sounds, lights",
		},
		{
			// Enter is no answer on an empty Other; with nothing checked it
			// takes the option under the cursor. Alt+x is not Esc.
			"enter",
			[]string{"3", keyEnterSeq, "\x1bx", "x", keyEnterSeq, "2", "2", keyLeftSeq, "\x1b[C", keyEnterSeq, keyEnterSeq},
			[]agent.Answer{{Other: "x"}, {Picked: []string{"Icons"}}},
			"  Approach: x\n  Features: Icons",
		},
		{"esc", []string{keyDownSeq, keyEnterSeq, "\x1b"}, nil, ""},
		{"keys at once", []string{keyDownSeq + keyEnterSeq + "2" + keyEnterSeq + "x"}, []agent.Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Icons"}}}, "  Approach: Patch\n  Features: Icons"},
	} {
		f := newForm(twoQuestions())
		done := false
		for i, k := range tc.keys {
			if done {
				t.Errorf("%s: done before key %d", tc.name, i)
			}
			_, done = f.feed([]byte(k))
		}
		if !done {
			t.Errorf("%s: not done", tc.name)
		}
		if got := f.answers(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: answers %+v, want %+v", tc.name, got, tc.want)
		}
		if got := capture.Clean([]byte(f.frame(80, 24))); got != tc.summary {
			t.Errorf("%s: last frame %q, want %q", tc.name, got, tc.summary)
		}
	}
}

// The frame shows where the user is: the question of how many, the
// options and the cursor, what is checked and typed, the keys that work.
func TestFormFrame(t *testing.T) {
	f := newForm(twoQuestions())
	frame := func() string { return capture.Clean([]byte(f.frame(80, 24))) }
	expect := func(step string, want, not []string) {
		t.Helper()
		got := frame()
		for _, s := range want {
			if !strings.Contains(got, s) {
				t.Errorf("%s: no %q in\n%s", step, s, got)
			}
		}
		for _, s := range not {
			if strings.Contains(got, s) {
				t.Errorf("%s: %q in\n%s", step, s, got)
			}
		}
	}
	expect("first", []string{"Approach 1/2", "Which approach?", "❯ 1. Rewrite", "Start from scratch", "    2. Patch", "    3. Other", "1-3 choose", "enter next", "esc cancel"}, []string{"back", "[ ]"})
	if f.answers() != nil {
		t.Error("answers before the form is done")
	}
	f.feed([]byte(keyEnterSeq))
	expect("second", []string{"Features 2/2", "Which features?", "❯ 1. [ ] Colors", "2. [ ] Icons", "4. [ ] Other", "space/1-4 check", "enter done", "← back"}, []string{"1/2"})
	f.feed([]byte("1"))
	expect("checked", []string{"❯ 1. [x] Colors"}, nil)
	f.feed([]byte("4ab"))
	expect("typing", []string{"    1. [x] Colors", "❯ 4. [x] Other: ab", "type the answer"}, nil)
	if !strings.Contains(f.frame(80, 24), "Other: "+reset+"ab"+reverse+" "+reset) {
		t.Errorf("no cursor after the text: %q", f.frame(80, 24))
	}
}

// Every line of a question fits in the terminal but its last column, even
// a long question, and the model's control characters are not drawn.
func TestFormFrameNarrow(t *testing.T) {
	qs := []agent.Question{{
		Question: "Which of these rather long-winded and verbose approaches should I take for the refactoring?",
		Header:   "Approach\x1b[31m",
		Options:  []agent.Option{{Label: "Rewrite everything from the very beginning", Description: "Slow but clean, and a description that wraps"}, {Label: "Patch"}},
	}}
	f := newForm(qs)
	f.feed([]byte("3" + strings.Repeat("typed ", 10)))
	const cols = 24
	frame := f.frame(cols, 24)
	for _, l := range strings.Split(frame, "\r\n") {
		if w := frameWidth(l); w > cols-1 {
			t.Errorf("%d columns: %q", w, l)
		}
	}
	got := capture.Clean([]byte(frame))
	for _, word := range strings.Fields(qs[0].Question) {
		if !strings.Contains(got, word) {
			t.Errorf("the question lost %q:\n%s", word, got)
		}
	}
	if strings.Contains(frame, "\x1b[31m") {
		t.Errorf("the header's escape drawn: %q", frame)
	}
	if !strings.Contains(got, "typed") || strings.Contains(got, "Other: typed typed") {
		t.Errorf("the end of the typed text is not what shows:\n%s", got)
	}
}

// feed takes the keys of the form alone: what comes after the Enter that
// answers it is not the form's.
func TestFormFeedRest(t *testing.T) {
	for _, tc := range []struct {
		keys string
		n    int
		done bool
	}{
		{"1\r" + "ls\r", 2, true},
		{keyDownSeq + keyEnterSeq + "ls", 4, true},
		{"2" + keyUpSeq, 4, false},
	} {
		n, done := newForm(twoQuestions()[:1]).feed([]byte(tc.keys))
		if n != tc.n || done != tc.done {
			t.Errorf("%q: took %d, done %v; want %d, %v", tc.keys, n, done, tc.n, tc.done)
		}
	}
}

// fourOptions is a question of four options whose descriptions wrap at 80
// columns: sixteen lines in all.
func fourOptions() []agent.Question {
	return []agent.Question{{
		Question: "Which database should the service keep its sessions in?",
		Header:   "Store",
		Options: []agent.Option{
			{Label: "Postgres", Description: "Already runs in production: sessions go to a table of their own, with an index on the expiry time and a nightly job to drop the stale ones."},
			{Label: "Redis", Description: "Fast and made for this, keys expire on their own, but it is one more server to run, back up and watch over at night."},
			{Label: "SQLite", Description: "A file next to the service: nothing to run, but every replica would keep sessions of its own, and that breaks the logins."},
			{Label: "Memory", Description: "Simplest of all, and every restart logs everyone out, which is fine for now and a problem later on."},
		},
	}}
}

// A question that fits on the screen is drawn whole, as it was before the
// frame knew the height.
func TestFormFrameFits(t *testing.T) {
	want := []string{
		"\x1b[1m  Store\x1b[0m\x1b[2m 1/1\x1b[0m",
		"  Which database should the service keep its sessions in?",
		"\x1b[36m  ❯ 1. Postgres\x1b[0m",
		"\x1b[2m       Already runs in production: sessions go to a table of their own, with an\x1b[0m",
		"\x1b[2m       index on the expiry time and a nightly job to drop the stale ones.\x1b[0m",
		"    2. Redis",
		"\x1b[2m       Fast and made for this, keys expire on their own, but it is one more\x1b[0m",
		"\x1b[2m       server to run, back up and watch over at night.\x1b[0m",
		"    3. SQLite",
		"\x1b[2m       A file next to the service: nothing to run, but every replica would keep\x1b[0m",
		"\x1b[2m       sessions of its own, and that breaks the logins.\x1b[0m",
		"    4. Memory",
		"\x1b[2m       Simplest of all, and every restart logs everyone out, which is fine for\x1b[0m",
		"\x1b[2m       now and a problem later on.\x1b[0m",
		"    5. Other",
		"\x1b[2m  ↑↓/1-5 choose · enter done · esc cancel\x1b[0m",
	}
	f := newForm(fourOptions())
	for _, rows := range []int{0, len(want) + 1, 100} {
		if got := strings.Split(f.frame(80, rows), "\r\n"); !reflect.DeepEqual(got, want) {
			t.Errorf("%d rows:\n%s", rows, strings.Join(got, "\n"))
		}
	}
}

// A frame taller than the screen loses the descriptions of the options
// but the one under the cursor, then the options far from the cursor, then
// the end of the question: the header, the option under the cursor and
// the hint stay.
func TestFormFrameHeight(t *testing.T) {
	lines := func(f *form, rows int) []string {
		t.Helper()
		got := strings.Split(capture.Clean([]byte(f.frame(80, rows))), "\n")
		if len(got) > rows-1 {
			t.Errorf("%d lines in %d rows:\n%s", len(got), rows, strings.Join(got, "\n"))
		}
		return got
	}
	expect := func(step string, got, want, not []string) {
		t.Helper()
		text := strings.Join(got, "\n")
		for _, s := range want {
			if !strings.Contains(text, s) {
				t.Errorf("%s: no %q in\n%s", step, s, text)
			}
		}
		for _, s := range not {
			if strings.Contains(text, s) {
				t.Errorf("%s: %q in\n%s", step, s, text)
			}
		}
	}

	f := newForm(fourOptions())
	expect("descriptions", lines(f, 13), []string{"Which database", "❯ 1. Postgres", "Already runs", "4. Memory", "5. Other", "esc cancel"}, []string{"Fast and made", "more"})
	expect("window", lines(f, 10), []string{"Store 1/1", "Which database", "❯ 1. Postgres", "Already runs", "3. SQLite", "↓ 2 more", "esc cancel"}, []string{"↑ ", "Fast and made"})
	f.feed([]byte("3"))
	expect("middle", lines(f, 10), []string{"↑ 2 more", "❯ 3. SQLite", "A file next to", "5. Other", "esc cancel"}, []string{"2. Redis", "↓ "})
	f.feed([]byte("5ab"))
	expect("typing", lines(f, 7), []string{"Store 1/1", "❯ 5. Other: ab", "type the answer"}, nil)

	long := fourOptions()
	long[0].Question = strings.Repeat("Which database should the service keep its sessions in? ", 10)
	f = newForm(long)
	got := lines(f, 10)
	expect("long question", got, []string{"Store 1/1", "❯ 1. Postgres", "↓ 3 more", "esc cancel"}, []string{"Already runs"})
	if len(got) != 9 || !strings.HasSuffix(got[4], "…") || strings.HasPrefix(got[5], "  Which") {
		t.Errorf("the question is not cut at its fourth line:\n%s", strings.Join(got, "\n"))
	}

	// Whatever the height and the option under the cursor.
	for _, qs := range [][]agent.Question{fourOptions(), long} {
		for rows := 6; rows <= 20; rows++ {
			for cur := 1; cur <= 5; cur++ {
				f := newForm(qs)
				f.feed([]byte{byte('0' + cur)})
				expect(fmt.Sprintf("%d rows, option %d", rows, cur), lines(f, rows), []string{"Store 1/1", fmt.Sprintf("❯ %d.", cur), "esc cancel"}, nil)
			}
		}
	}
}

// formProxy is a proxy whose terminal is cols wide, with a form open.
func formProxy(t *testing.T, cols *int) (*Proxy, *terminal, chan formResult, context.CancelFunc) {
	t.Helper()
	return formProxyOf(t, cols, twoQuestions())
}

// formProxyOf is formProxy with the form of qs.
func formProxyOf(t *testing.T, cols *int, qs []agent.Question) (*Proxy, *terminal, chan formResult, context.CancelFunc) {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	p.size = func() (int, int) { return *cols, 24 }
	p.at = &statusAt{col: 10, cols: *cols} // left by a call before; the form's line is closed
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan formResult, 1)
	go func() {
		ans, err := p.askForm(ctx, qs)
		res <- formResult{ans, err}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		open := p.form != nil
		p.mu.Unlock()
		if open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the form never opened")
		}
	}
	return p, out, res, cancel
}

type formResult struct {
	ans []agent.Answer
	err error
}

func result(t *testing.T, res chan formResult) formResult {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the form never returned")
	}
	return formResult{}
}

// up is how a redraw goes back to the first line of the frame drawn and
// clears the screen from there: never erase below from the line's start
// itself, which tmux takes for a clear screen when the line is the top one.
func up(lines int) string { return fmt.Sprintf("\r\x1b[%dA\x1b[K\x1b[B\x1b[J\x1b[A", lines-1) }

// While the form is open the keys are its own, but Ctrl+C, which stops
// the request; once answered, the summary replaces it on the screen.
func TestFormKeys(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxy(t, &cols)
	defer cancel()
	if p.at != nil {
		t.Error("a status position left for the form's line")
	}
	if got := p.key([]byte(keyDownSeq + keyEnterSeq)); got != nil {
		t.Errorf("keys went to the shell: %q", got)
	}
	if got := p.key([]byte{'2', 0x03}); !bytes.Equal(got, []byte{0x03}) {
		t.Errorf("Ctrl+C did not reach the shell: %q", got)
	}
	shown := len(p.form.shown)
	before := len(out.String())
	if got := p.key([]byte(keyEnterSeq)); got != nil {
		t.Errorf("enter went to the shell: %q", got)
	}
	r := result(t, res)
	if want := []agent.Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Icons"}}}; r.err != nil || !reflect.DeepEqual(r.ans, want) {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}
	s := out.String()
	if clean := capture.Clean([]byte(s)); !strings.HasPrefix(s, "\x1b[?25l") || !strings.Contains(clean, "Approach 1/2") || !strings.Contains(clean, "Features 2/2") {
		t.Errorf("terminal %q", s)
	}
	last := s[before:]
	if want := up(shown) + dim + "  Approach:" + reset + " Patch\r\n" + dim + "  Features:" + reset + " Icons\r\n\x1b[?25h"; last != want {
		t.Errorf("closed with %q, want %q", last, want)
	}
	if p.form != nil {
		t.Error("the form stayed open")
	}
	if got := p.key([]byte("ls")); string(got) != "ls" {
		t.Errorf("after the form: %q", got)
	}
}

// Esc ends the form with no answers and nothing left of it; ctx, with the
// form gone from the screen too.
func TestFormCancel(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxy(t, &cols)
	defer cancel()
	p.key([]byte("\x1b"))
	if r := result(t, res); r.ans != nil || r.err != nil {
		t.Errorf("esc: %+v", r)
	}
	if s := out.String(); !strings.HasSuffix(s, "\x1b[J\x1b[A\x1b[?25h") || p.form != nil {
		t.Errorf("esc left %q, form %v", s, p.form)
	}

	p, out, res, cancel = formProxy(t, &cols)
	cancel()
	if r := result(t, res); !errors.Is(r.err, context.Canceled) {
		t.Errorf("interrupted: %+v", r)
	}
	if s := out.String(); !strings.HasSuffix(s, "\x1b[J\x1b[A\x1b[?25h") || p.form != nil {
		t.Errorf("interrupted, left %q, form %v", s, p.form)
	}

	sess, _ := session.New(t.TempDir())
	if _, err := New(sess).askForm(context.Background(), twoQuestions()); err == nil {
		t.Error("a form without a terminal")
	}
}

// A narrowed terminal wraps the lines of the frame: the redraw counts the
// rows they take now.
func TestFormResized(t *testing.T) {
	cols := 80
	p, out, _, cancel := formProxy(t, &cols)
	defer cancel()
	rows := 0
	cols = 10
	for _, l := range p.form.shown {
		rows += (frameWidth(l) + cols - 1) / cols
	}
	before := len(out.String())
	p.resized()
	if got := out.String()[before:]; !strings.HasPrefix(got, up(rows)) {
		t.Errorf("redrawn with %q, want %q first", got, up(rows))
	}
	for _, l := range p.form.shown {
		if w := frameWidth(l); w > cols-1 {
			t.Errorf("%d columns after the resize: %q", w, l)
		}
	}
}

// Ctrl+O opens the viewer over the form, which waits for it to close.
func TestFormViewer(t *testing.T) {
	cols := 80
	p, _, res, cancel := formProxy(t, &cols)
	defer cancel()
	p.mu.Lock()
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.mu.Unlock()
	p.key([]byte(keyDownSeq + "\x0f" + keyEnterSeq))
	if p.view == nil {
		t.Fatal("no viewer")
	}
	if p.form.f.cur[0] != 1 || p.form.f.step != 0 {
		t.Errorf("the keys around Ctrl+O: cur %d, step %d", p.form.f.cur[0], p.form.f.step)
	}
	p.key([]byte{0x0f})
	if p.view != nil || p.form == nil {
		t.Fatalf("viewer %v, form %v", p.view, p.form)
	}
	p.key([]byte(keyEnterSeq + keyEnterSeq))
	if r := result(t, res); r.err != nil || len(r.ans) != 2 {
		t.Errorf("after the viewer: %+v", r)
	}
}
