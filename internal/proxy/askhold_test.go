package proxy

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
)

// heldOutput is the shell's output the open question holds.
func heldOutput(p *Proxy) string {
	return locked(p, func() string {
		if h := p.openHold(); h != nil {
			return string(h.out)
		}
		return ""
	})
}

// askOf opens the question q on p, with ctx, and waits till it is open.
func askOf(t *testing.T, p *Proxy, ctx context.Context, q string) chan askResult {
	t.Helper()
	res := make(chan askResult, 1)
	go func() {
		ans, err := p.askUser(ctx, q)
		res <- askResult{ans, err}
	}()
	waitOpen(t, p, func() bool { return p.ask != nil })
	return res
}

func askEnded(t *testing.T, res chan askResult) askResult {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the question stayed open")
	}
	return askResult{}
}

// vtLines is the screen of a terminal cols wide after s.
func vtLines(cols int, s string) []string {
	v := newVT(cols)
	v.play(s)
	return v.lines()
}

// A job in the background writes while the question is open, one line
// over the question too: none of it is drawn, and after the answer all of
// it is, in the order it came, below the line the answer left.
func TestAskHoldsOutput(t *testing.T) {
	p, out := termProxy(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := askOf(t, p, ctx, "allow rm -rf build?")
	drawn := out.String()
	chunks := []string{"job 1\r\n", "\r\x1b[Kallow ls?", " \x1b[7m[ Yes ]\x1b[27m", "\r\njob 2\r\n"}
	for _, c := range chunks {
		p.output([]byte(c))
	}
	if s := out.String(); s != drawn {
		t.Errorf("drawn under the question: %q", strings.TrimPrefix(s, drawn))
	}
	if got := vtLines(80, out.String()); !reflect.DeepEqual(got, []string{"allow rm -rf build? [ Yes ]   No"}) {
		t.Errorf("the screen under the question:\n%s", strings.Join(got, "\n"))
	}
	if locked(p, p.holding) {
		t.Error("the held output counts as the question out of sight") // its clock would stand
	}

	pause(p) // the question's keys come after one (askguard.go)
	if pass := p.key([]byte("n")); len(pass) != 0 {
		t.Errorf("to the shell: %q", pass)
	}
	if r := askEnded(t, res); r.ans != "n" || r.err != nil {
		t.Errorf("answered %q, %v", r.ans, r.err)
	}
	after := modeless(strings.TrimPrefix(out.String(), drawn))
	if want := "No\x1b[K\r\n\x1b[?25h" + strings.Join(chunks, ""); !strings.HasSuffix(after, want) {
		t.Errorf("after the answer: %q, want it to end in %q", after, want)
	}
	want := []string{"allow rm -rf build? No", "job 1", "allow ls? [ Yes ]", "job 2", ""}
	if got := vtLines(80, out.String()); !reflect.DeepEqual(got, want) {
		t.Errorf("the screen after the answer:\n%s", strings.Join(got, "\n"))
	}
	if heldOutput(p) != "" || locked(p, func() bool { return p.ask != nil }) {
		t.Error("the question stayed")
	}

	// The question gone, the output is the screen's at once.
	before := len(out.String())
	p.output([]byte("job 3\r\n"))
	if s := out.String()[before:]; s != "job 3\r\n" {
		t.Errorf("after the question: %q", s)
	}
}

// The form holds the output as the question does; it comes after the
// summary of the answers.
func TestFormHoldsOutput(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	drawn := out.String()
	p.output([]byte("job 1\r\n"))
	p.output([]byte("\x1b[2A\r\x1b[KWhich file to delete?\r\n"))
	if s := out.String(); s != drawn {
		t.Errorf("drawn under the form: %q", strings.TrimPrefix(s, drawn))
	}
	pause(p) // the form's keys come after one (askguard.go)
	p.key([]byte("1\r"))
	if r := result(t, res); r.err != nil || len(r.ans) != 1 {
		t.Fatalf("answers %+v, %v", r.ans, r.err)
	}
	after := strings.TrimPrefix(out.String(), drawn)
	i, j := strings.Index(after, "Rewrite"), strings.Index(after, "job 1\r\n\x1b[2A\r\x1b[KWhich file to delete?\r\n")
	if i < 0 || j < i {
		t.Errorf("the summary at %d, the output at %d: %q", i, j, after)
	}
}

// Ctrl+C in the question: the output that came before it waits for the
// erase, and goes below the call where the question was; the echo of ^C,
// with the keys typed ahead that went to the shell before it, goes with
// the question, as it would with no hold; what came after the echo goes
// after what came before. However many lines the question takes.
func TestAskHoldCtrlC(t *testing.T) {
	const call = "❯ git push --force origin main\r\n"
	reason := "force push rewrites the remote history of a shared branch; everyone who pulled it will have to rebase"
	for _, cols := range []int{40, 80, 200} {
		p, out := termProxy(t)
		p.size = func() (int, int) { return cols, 24 }
		ctx, cancel := context.WithCancel(context.Background())
		res := askOf(t, p, ctx, reason+" - allow?")
		p.output([]byte("job 1\r\n"))
		atOnce(p)
		p.key([]byte("ls")) // typed ahead, for the shell
		if pass := p.key([]byte{0x03}); string(pass) != "ls\x03" {
			t.Errorf("%d columns: to the shell %q", cols, pass)
		}
		p.output([]byte("ls^C"))
		p.output([]byte("job 2\r\n"))
		cancel()
		if r := askEnded(t, res); !errors.Is(r.err, context.Canceled) {
			t.Errorf("%d columns: %v", cols, r.err)
		}
		v := newVT(cols)
		v.play(call + out.String())
		if got, want := v.lines(), []string{"❯ git push --force origin main", "job 1", "job 2", ""}; !reflect.DeepEqual(got, want) {
			t.Errorf("%d columns, the screen:\n%s", cols, strings.Join(got, "\n"))
		}
		if v.row != 3 || v.col != 0 {
			t.Errorf("%d columns: the prompt starts at row %d, column %d", cols, v.row, v.col)
		}
	}
}

// Ctrl+C in the form, as in the question.
func TestFormHoldCtrlC(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxyOf(t, &cols, twoQuestions())
	p.output([]byte("job 1\r\n"))
	if pass := p.key([]byte{0x03}); string(pass) != "\x03" {
		t.Errorf("to the shell %q", pass)
	}
	p.output([]byte("^Cjob 2\r\n"))
	cancel()
	if r := result(t, res); !errors.Is(r.err, context.Canceled) {
		t.Errorf("the form ended with %v", r.err)
	}
	v := newVT(cols)
	v.play(out.String())
	if got, want := v.lines(), []string{"job 1", "job 2", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("the screen:\n%s", strings.Join(got, "\n"))
	}
}

// The question that ends with no answer in time leaves its line with No,
// and the output below it. The output held does not stop its clock, as
// the viewer does.
func TestAskHoldTimeout(t *testing.T) {
	p, out := termProxy(t)
	ctx := agent.WithAnswerTime(context.Background(), 50*time.Millisecond)
	start := time.Now()
	res := askOf(t, p, ctx, "allow?")
	p.output([]byte("job 1\r\n"))
	r := askEnded(t, res)
	if !errors.Is(r.err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Errorf("ended with %v after %v", r.err, time.Since(start))
	}
	if got, want := vtLines(80, out.String()), []string{"allow? No (no answer in 50ms)", "job 1", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("the screen:\n%s", strings.Join(got, "\n"))
	}
}

// The viewer opened over the question gives back the screen with what it
// held, the question's frames; the shell's output waits on for the
// question.
func TestAskHoldUnderViewer(t *testing.T) {
	p, out := termProxy(t)
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := askOf(t, p, ctx, "allow?")
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	p.output([]byte("job 1\r\n"))
	p.key([]byte("q"))
	if p.view != nil {
		t.Fatal("the viewer stayed")
	}
	if s := out.String(); strings.Contains(s, "job 1") {
		t.Errorf("drawn with the question open: %q", s)
	}
	pause(p)
	p.key([]byte("y"))
	if r := askEnded(t, res); r.ans != "y" {
		t.Errorf("answered %q, %v", r.ans, r.err)
	}
	if s := modeless(out.String()); !strings.HasSuffix(s, "Yes\x1b[K\r\n\x1b[?25hjob 1\r\n") {
		t.Errorf("after the answer: %q", s)
	}
}

// What a Ctrl+C passed on is echoed as goes before the erase: text on the
// line up to the last ^C, nothing that moves the cursor or is no echo.
func TestAskHoldEcho(t *testing.T) {
	for _, tc := range []struct {
		before, after string // the output before Ctrl+C and after it
		echo, rest    string
	}{
		{"", "^C", "^C", ""},
		{"job\r\n", "^C", "^C", "job\r\n"},
		{"", "ls^C^Cjob\r\n", "ls^C^C", "job\r\n"},
		{"", "дир^C", "дир^C", ""},
		{"", "job\r\n^C", "", "job\r\n^C"},
		{"", "\x1b[31m^C", "", "\x1b[31m^C"},
		{"^C", "", "", "^C"},
		{"", "no echo", "", "no echo"},
	} {
		h := askHold{}
		h.add([]byte(tc.before))
		h.interrupted()
		h.add([]byte(tc.after))
		h.interrupted() // a second Ctrl+C: the echo begins at the first
		if e := string(h.echo()); e != tc.echo || string(h.out) != tc.rest {
			t.Errorf("%q, %q: echo %q, rest %q", tc.before, tc.after, e, h.out)
		}
		if e := h.echo(); e != nil {
			t.Errorf("%q, %q: echoed again %q", tc.before, tc.after, e)
		}
	}

	var h askHold
	if h.echo() != nil {
		t.Error("an echo with no Ctrl+C")
	}
}

// A job writing on while nobody answers takes no more than twice the cap:
// the end of what it wrote stays, the echo with it if it is there.
func TestAskHoldCap(t *testing.T) {
	var h askHold
	chunk := bytes.Repeat([]byte("x"), 32<<10)
	for range 3 * askHoldCap / len(chunk) {
		h.add(chunk)
	}
	h.interrupted()
	h.add([]byte("^Cend"))
	if n := len(h.out); n > 2*askHoldCap || n < askHoldCap || !bytes.HasSuffix(h.out, []byte("x^Cend")) {
		t.Errorf("kept %d bytes, ending in %q", n, h.out[max(n-8, 0):])
	}
	for range askHoldCap / len(chunk) {
		h.add(chunk)
	}
	if e := h.echo(); string(e) != "^C" {
		t.Errorf("the echo kept: %q", e)
	}

	h = askHold{}
	h.interrupted()
	h.add([]byte("^C"))
	for range 3 * askHoldCap / len(chunk) {
		h.add(chunk)
	}
	if e := h.echo(); e != nil || bytes.Contains(h.out, []byte("^C")) {
		t.Errorf("an echo dropped: %q", e)
	}
}
