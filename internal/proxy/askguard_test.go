package proxy

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
)

// pause is the user waiting before the next key: the keys come askGrace
// after the last read, and all at once from then on till the next pause.
func pause(p *Proxy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at := p.keyTime().Add(askGrace)
	p.keyClock = func() time.Time { return at }
}

// atOnce stops the clock of the keys: what is typed from now on comes with
// no pause, till the next one.
func atOnce(p *Proxy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at := p.keyTime()
	p.keyClock = func() time.Time { return at }
}

// asking opens the question allow? on p and waits till it is open.
func asking(t *testing.T, p *Proxy, ctx context.Context) chan askResult {
	t.Helper()
	res := make(chan askResult, 1)
	go func() {
		ans, err := p.askUser(ctx, "allow?")
		res <- askResult{ans, err}
	}()
	waitOpen(t, p, func() bool { return p.ask != nil })
	return res
}

// typed gives each of keys to p as a read of its own and returns what went
// on to the shell.
func typed(p *Proxy, keys ...string) string {
	var shell []byte
	for _, k := range keys {
		shell = append(shell, p.key([]byte(k))...)
	}
	return string(shell)
}

// still checks that the question is open and nothing more was drawn since
// before.
func still(t *testing.T, p *Proxy, out *terminal, before int, what string) {
	t.Helper()
	p.mu.Lock()
	open := p.ask != nil || p.form != nil
	p.mu.Unlock()
	if !open {
		t.Fatalf("%s answered the question", what)
	}
	if s := out.String()[before:]; s != "" {
		t.Errorf("%s drew %q", what, s)
	}
}

// pwd⏎ typed on as the question opens does not allow the call the
// question is about: the question stays as it was, and once it is
// answered pwd⏎ goes on to the shell, ahead of what was typed after the
// answer.
func TestAskTypeahead(t *testing.T) {
	for _, reads := range [][]string{{"p", "w", "d", "\r"}, {"pwd\r"}} {
		p, out := termProxy(t)
		atOnce(p)
		res := asking(t, p, context.Background())
		before := len(out.String())
		if got := typed(p, reads...); got != "" {
			t.Errorf("%q went to the shell at once: %q", reads, got)
		}
		still(t, p, out, before, "pwd⏎")
		pause(p)
		if got := typed(p, "nls\r"); got != "pwd\rls\r" {
			t.Errorf("%q: the shell got %q after the answer, want pwd⏎ first", reads, got)
		}
		if r := <-res; r.ans != "n" || r.err != nil {
			t.Errorf("%q: answered %q, %v", reads, r.ans, r.err)
		}
	}
}

// cd mydir, the question opening after cd m: y is no answer, nor is
// anything typed on; the shell gets the whole line.
func TestAskTypeaheadY(t *testing.T) {
	p, out := termProxy(t)
	atOnce(p)
	shell := typed(p, "c", "d", " ", "m")
	res := asking(t, p, context.Background())
	before := len(out.String())
	if got := typed(p, "y", "d", "i", "r"); got != "" {
		t.Errorf("went to the shell at once: %q", got)
	}
	still(t, p, out, before, "ydir")
	pause(p)
	shell += typed(p, "n")
	if shell != "cd mydir" {
		t.Errorf("the shell got %q", shell)
	}
	if r := <-res; r.ans != "n" {
		t.Errorf("answered %q", r.ans)
	}
}

// After a pause y answers Yes, Enter the choice, Alt+B nothing.
func TestAskAfterPause(t *testing.T) {
	p, out := termProxy(t)
	for _, tc := range []struct {
		keys []string // each after a pause
		ans  string
	}{
		{[]string{"y"}, "y"},
		{[]string{"\r"}, "y"},
		{[]string{"\x1b[C", "\r"}, "n"},
		{[]string{"\x1bb", "N"}, "n"},
	} {
		res := asking(t, p, context.Background())
		var shell string
		for i, k := range tc.keys {
			pause(p)
			before := len(out.String())
			shell += typed(p, k)
			if i < len(tc.keys)-1 && k == "\x1bb" {
				still(t, p, out, before, "Alt+B")
			}
		}
		if r := <-res; r.ans != tc.ans || r.err != nil {
			t.Errorf("%q: answered %q, %v", tc.keys, r.ans, r.err)
		}
		if shell != "" {
			t.Errorf("%q: the shell got %q", tc.keys, shell)
		}
	}

	// Alt+B typed on is the shell's, as the rest.
	atOnce(p)
	res := asking(t, p, context.Background())
	typed(p, "\x1bb", "x")
	pause(p)
	if got := typed(p, "y"); got != "\x1bbx" {
		t.Errorf("the shell got %q", got)
	}
	<-res
}

// To Yes/No a key right after one of its own is nothing, neither an
// answer nor the shell's: of ↑↑⏎ typed on after a pause, the first ↑ turns
// the answer, and ↑⏎ at the shell would run another command than the one
// meant.
func TestAskKeyThenKey(t *testing.T) {
	p, out := termProxy(t)
	res := asking(t, p, context.Background())
	pause(p)
	typed(p, "\x1b[A")
	before := len(out.String())
	if got := typed(p, "\x1b[A", "\r"); got != "" {
		t.Errorf("the shell got %q", got)
	}
	still(t, p, out, before, "↑⏎ right after ↑")
	pause(p)
	if got := typed(p, "\r"); got != "" {
		t.Errorf("the shell got %q", got)
	}
	if r := <-res; r.ans != "n" {
		t.Errorf("answered %q", r.ans)
	}

	// A character the question has no use for begins a line for the
	// shell: it and what is typed on after it wait for the shell.
	res = asking(t, p, context.Background())
	pause(p)
	before = len(out.String())
	if got := typed(p, "g", "i", "t", " ", "l", "o", "g", "\r"); got != "" {
		t.Errorf("went to the shell at once: %q", got)
	}
	still(t, p, out, before, "git log⏎ after a pause")
	pause(p)
	if got := typed(p, "y"); got != "git log\r" {
		t.Errorf("the shell got %q", got)
	}
	<-res
}

// A paste is typing too: Enter right after it is no answer.
func TestAskAfterPaste(t *testing.T) {
	for _, reads := range [][]string{{pasted, "\r"}, {pasted + "\r"}} {
		p, out := termProxy(t)
		res := asking(t, p, context.Background())
		pause(p)
		before := len(out.String())
		typed(p, reads...)
		still(t, p, out, before, "Enter after a paste")
		pause(p)
		if got := typed(p, "n"); got != "\r" {
			t.Errorf("%q: the shell got %q", reads, got)
		}
		<-res
	}
}

// Ctrl+C goes at once, after what was typed ahead of it; Ctrl+O opens the
// viewer at once.
func TestAskTypeaheadCtrlC(t *testing.T) {
	p, out := termProxy(t)
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	ctx, cancel := context.WithCancel(context.Background())
	atOnce(p)
	res := asking(t, p, ctx)
	if got := typed(p, "ls", "\x03"); got != "ls\x03" {
		t.Errorf("Ctrl+C: the shell got %q", got)
	}
	typed(p, "x", "\x0f")
	if !locked(p, func() bool { return p.view != nil }) {
		t.Error("Ctrl+O typed on opened no viewer")
	}
	typed(p, "q")
	toShell := &terminal{}
	p.mu.Lock()
	p.toShell = toShell
	p.mu.Unlock()
	before := len(out.String())
	typed(p, "y")
	still(t, p, out, before, "y right after the viewer")
	cancel()
	if r := <-res; !errors.Is(r.err, context.Canceled) {
		t.Errorf("answered %q, %v", r.ans, r.err)
	}
	// Ended without a key: what waited goes to the shell by itself.
	if s := toShell.String(); s != "xy" {
		t.Errorf("the shell got %q", s)
	}
}

// A question out of time gives the shell what was typed ahead of it.
func TestAskTypeaheadTimeout(t *testing.T) {
	p, _ := termProxy(t)
	toShell := &terminal{}
	p.toShell = toShell
	atOnce(p)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res := asking(t, p, ctx)
	typed(p, "pwd\r")
	if r := <-res; !errors.Is(r.err, context.DeadlineExceeded) {
		t.Errorf("answered %q, %v", r.ans, r.err)
	}
	if s := toShell.String(); s != "pwd\r" {
		t.Errorf("the shell got %q", s)
	}
}

// A question that opens under the viewer is shown when the viewer
// closes: a key right after that is typed on, not an answer.
func TestAskUnderViewer(t *testing.T) {
	p, out := termProxy(t)
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.key([]byte{ctrlO})
	res := asking(t, p, context.Background())
	pause(p)
	typed(p, "q")
	before := len(out.String())
	typed(p, "y")
	still(t, p, out, before, "y right after the viewer")
	pause(p)
	if got := typed(p, "n"); got != "y" {
		t.Errorf("the shell got %q", got)
	}
	if r := <-res; r.ans != "n" {
		t.Errorf("answered %q", r.ans)
	}
}

// A number and Enter typed on as the form opens answer nothing: the form
// stays as it was and they go on to the shell once it is answered.
func TestFormTypeahead(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	atOnce(p)
	before := len(out.String())
	if got := typed(p, "2", "\r"); got != "" {
		t.Errorf("went to the shell at once: %q", got)
	}
	still(t, p, out, before, "2⏎")
	pause(p)
	if got := typed(p, "1\rls"); got != "2\rls" {
		t.Errorf("the shell got %q", got)
	}
	if r := result(t, res); r.err != nil || !reflect.DeepEqual(r.ans, []agent.Answer{{Picked: []string{"Rewrite"}}}) {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}
}

// After the pause the form reads the keys as they come: Other is typed as
// fast as the user types. A letter on an option is typed ahead, with what
// comes right after it.
func TestFormAfterPause(t *testing.T) {
	cols := 80
	p, _, res, cancel := formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	pause(p)
	if got := typed(p, keyDownSeq, keyDownSeq, "m", "y", " ", "w", "a", "y", "\r"); got != "" {
		t.Errorf("the shell got %q", got)
	}
	if r := result(t, res); r.err != nil || !reflect.DeepEqual(r.ans, []agent.Answer{{Other: "my way"}}) {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}

	p, out, res, cancel := formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	pause(p)
	before := len(out.String())
	if got := typed(p, "p", "w", "d", "\r"); got != "" {
		t.Errorf("went to the shell at once: %q", got)
	}
	still(t, p, out, before, "pwd⏎ after a pause")
	pause(p)
	if got := typed(p, "\r"); got != "pwd\r" {
		t.Errorf("the shell got %q", got)
	}
	if r := result(t, res); r.err != nil || !reflect.DeepEqual(r.ans, []agent.Answer{{Picked: []string{"Rewrite"}}}) {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}
}

// The question of aish yolo, past its first firmWait, takes no Yes typed
// on either: the y of yes⏎ goes nowhere, and the rest answers No, as any
// key that is no answer to it does (confirm.go).
func TestFirmTypeahead(t *testing.T) {
	firmNow(t)
	p, out, _ := hosted(t, &scripted{})
	res := askYolo(t, p, context.Background())
	atOnce(p)
	before := len(out.String())
	if pass := typed(p, "y"); pass != "" {
		t.Errorf("to the shell %q", pass)
	}
	still(t, p, out, before, "y typed on")
	if pass := typed(p, "es\r"); pass != "" {
		t.Errorf("to the shell %q", pass)
	}
	if err := answered(t, res); !errors.Is(err, errYoloDeclined) || p.yoloOn() {
		t.Errorf("%v, on %v", err, p.yoloOn())
	}
}

// The form ended with no key gives the shell what was typed ahead of it.
func TestFormTypeaheadEnd(t *testing.T) {
	cols := 80
	p, _, res, cancel := formProxyOf(t, &cols, oneQuestion())
	toShell := &terminal{}
	p.mu.Lock()
	p.toShell = toShell
	p.mu.Unlock()
	atOnce(p)
	typed(p, "ls")
	cancel()
	if r := result(t, res); !errors.Is(r.err, context.Canceled) {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}
	if s := toShell.String(); s != "ls" {
		t.Errorf("the shell got %q", s)
	}
}
