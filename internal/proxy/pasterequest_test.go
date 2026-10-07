package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/session"
)

// modeless is what the terminal got without the bracketed paste modes the
// proxy sets while it reads the keys: the tests of what it draws leave
// them to the tests here.
func modeless(s string) string {
	return strings.NewReplacer(string(pasteOn), "", string(pasteOff), "").Replace(s)
}

// pasteModeOf is the bracketed paste mode s leaves the terminal in: "on",
// "off", or "" if s sets none.
func pasteModeOf(s string) string {
	on, off := strings.LastIndex(s, string(pasteOn)), strings.LastIndex(s, string(pasteOff))
	switch {
	case on > off:
		return "on"
	case off > on:
		return "off"
	}
	return ""
}

// questionProxy is a proxy whose shell's output was out, with the policy's
// question open; the answer comes on the channel.
func questionProxy(t *testing.T, out ...string) (*Proxy, *terminal, chan string, context.CancelFunc) {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	term := &terminal{}
	p.out = term
	p.size = func() (int, int) { return 80, 24 }
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	for _, b := range out {
		p.output([]byte(b))
	}
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan string, 1)
	go func() {
		ans, _ := p.askUser(ctx, "allow?")
		res <- ans
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		open := p.ask != nil
		p.mu.Unlock()
		if open {
			return p, term, res, cancel
		}
		if time.Now().After(deadline) {
			t.Fatal("the question never opened")
		}
	}
}

func answer(t *testing.T, res chan string) string {
	t.Helper()
	select {
	case a := <-res:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	return ""
}

// While the question is open the terminal brackets a paste, whatever mode
// the shell left it in; answered, the question gives the shell's mode back:
// off while a request runs, as readline turned it off before the line.
func TestPasteModeQuestion(t *testing.T) {
	for _, c := range []struct {
		name  string
		shell []string
		after string
	}{
		{"never set", nil, "off"},
		{"readline before the line", []string{"$ \x1b[?2004h", "\r\n\x1b[?2004l\r"}, "off"},
		{"on", []string{"\x1b[?2004h"}, "on"},
		{"cut", []string{"x\x1b[?20", "04h"}, "on"},
		{"cut after ESC", []string{"\x1b[?2004hx\x1b", "[?2004l"}, "off"},
		{"with other modes", []string{"\x1b[?1;2004;25h"}, "on"},
		{"another mode", []string{"\x1b[?2004h\x1b[?20041l\x1b[?12004l"}, "on"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, out, res, cancel := questionProxy(t, c.shell...)
			defer cancel()
			if m := pasteModeOf(out.String()); m != "on" {
				t.Fatalf("the question is open with the mode %q: %q", m, out.String())
			}
			before := len(out.String())
			if got := p.key([]byte("n")); len(got) != 0 {
				t.Errorf("went to the shell: %q", got)
			}
			if a := answer(t, res); a != "n" {
				t.Errorf("answer %q", a)
			}
			if m := pasteModeOf(out.String()[before:]); m != c.after {
				t.Errorf("answered, the mode %q, want %q: %q", m, c.after, out.String()[before:])
			}
		})
	}
}

// A paste into the question, brackets and all cut by the reads anywhere,
// answers nothing, and nothing of it, the newlines least of all, reaches
// the shell.
func TestPasteQuestion(t *testing.T) {
	defer func(w time.Duration) { escWait = w }(escWait)
	escWait = 10 * time.Millisecond

	const paste = "\x1b[200~yes\r\nls\r\x1b[201~"
	for cut, reads := range cuts(paste) {
		t.Run(cut, func(t *testing.T) {
			p, _, res, cancel := questionProxy(t)
			defer cancel()
			var shell strings.Builder
			for _, r := range reads {
				shell.Write(p.key([]byte(r)))
			}
			settled(p)
			if shell.Len() > 0 {
				t.Errorf("the shell got %q", shell.String())
			}
			select {
			case a := <-res:
				t.Fatalf("the paste answered %q", a)
			default:
			}
			p.mu.Lock()
			open := p.ask != nil
			p.mu.Unlock()
			if !open {
				t.Fatal("the question is gone")
			}
			if got := p.key([]byte("n")); len(got) != 0 {
				t.Errorf("went to the shell: %q", got)
			}
			if a := answer(t, res); a != "n" {
				t.Errorf("typed after the paste: %q", a)
			}
		})
	}
}

// The shell turning the mode off while the proxy reads the keys gets it
// turned on again after its output, not inside a sequence the output cut
// short; once the keys are the shell's again, its mode is back.
func TestPasteModeShellOff(t *testing.T) {
	p, out, res, cancel := questionProxy(t)
	defer cancel()
	p.output([]byte("job\x1b[?2004l"))
	if s := out.String(); pasteModeOf(s) != "on" || !strings.HasSuffix(s, "job\x1b[?2004l"+string(pasteOn)) {
		t.Errorf("the mode was not turned on again: %q", s)
	}
	before := len(out.String())
	p.output([]byte("\x1b[?2004h\x1b[?2004l\x1b[3"))
	if s := out.String()[before:]; s != "\x1b[?2004h\x1b[?2004l\x1b[3" {
		t.Errorf("into a sequence cut short: %q", s)
	}
	p.output([]byte("1mred"))
	if s := out.String()[before:]; !strings.HasSuffix(s, "\x1b[31mred"+string(pasteOn)) {
		t.Errorf("after the sequence: %q", s)
	}
	before = len(out.String())
	p.key([]byte("y"))
	answer(t, res)
	if m := pasteModeOf(out.String()[before:]); m != "off" {
		t.Errorf("answered, the mode %q: %q", m, out.String()[before:])
	}
	// The keys are the shell's: its mode is its own.
	before = len(out.String())
	p.output([]byte("\x1b[?2004h$ \x1b[?2004l"))
	if s := out.String()[before:]; s != "\x1b[?2004h$ \x1b[?2004l" {
		t.Errorf("at the prompt: %q", s)
	}
}

// The viewer, the panes and the form turn the mode on as the question
// does, and give the shell's back. Output the viewer held is written
// before: the mode the shell set in it is the shell's.
func TestPasteModeReaders(t *testing.T) {
	// The viewer while a request runs.
	p := keysProxy(t)
	out := p.out.(*terminal)
	p.asking = true
	p.output([]byte("\x1b[?2004l"))
	p.key([]byte{ctrlO})
	if p.view == nil || pasteModeOf(out.String()) != "on" {
		t.Fatalf("the viewer opened with the mode %q", pasteModeOf(out.String()))
	}
	before := len(out.String())
	p.key([]byte("q"))
	if s := out.String()[before:]; p.view != nil || pasteModeOf(s) != "off" {
		t.Errorf("the viewer closed with %q", s)
	}

	// The request ends while the viewer is open: readline's mode, held
	// with the prompt, stays.
	p.key([]byte{ctrlO})
	p.output([]byte("$ \x1b[?2004h"))
	p.asking = false
	before = len(out.String())
	p.key([]byte("q"))
	if s := out.String()[before:]; pasteModeOf(s) != "on" || !strings.Contains(s, "$ ") {
		t.Errorf("the viewer closed at the prompt with %q", s)
	}

	// At the prompt the shell has it on already.
	before = len(out.String())
	p.key([]byte{ctrlO})
	p.key([]byte{ctrlO})
	if s := out.String()[before:]; pasteModeOf(s) == "off" {
		t.Errorf("the viewer at the prompt turned it off: %q", s)
	}

	// The viewer over the question keeps it on.
	p, term, res, cancel := questionProxy(t)
	defer cancel()
	p.key([]byte{ctrlO})
	before = len(term.String())
	p.key([]byte{ctrlO})
	if s := term.String()[before:]; p.view != nil || pasteModeOf(s) == "off" {
		t.Errorf("the viewer over the question closed with %q", s)
	}
	before = len(term.String())
	p.key([]byte("y"))
	answer(t, res)
	if s := term.String()[before:]; pasteModeOf(s) != "off" {
		t.Errorf("answered: %q", s)
	}

	// The panes.
	p, term, u, _ := paneProxy(t)
	u.Pane("alpha")
	due(p)
	if s := term.String(); !strings.HasPrefix(s, panesOpen) || pasteModeOf(s) != "on" {
		t.Errorf("the panes opened with %q", s)
	}
	before = len(term.String())
	p.key([]byte("q"))
	if s := term.String()[before:]; pasteModeOf(s) != "off" {
		t.Errorf("the panes detached with %q", s)
	}
	p.key([]byte{ctrlO})
	if s := term.String(); pasteModeOf(s) != "on" {
		t.Errorf("the panes back with %q", s)
	}
	before = len(term.String())
	u.ClosePanes()
	if s := term.String()[before:]; pasteModeOf(s) != "off" {
		t.Errorf("the panes closed with %q", s)
	}

	// The form, answered and cancelled.
	cols := 80
	p, term, fres, fcancel := formProxy(t, &cols)
	defer fcancel()
	if s := term.String(); pasteModeOf(s) != "on" {
		t.Errorf("the form opened with %q", s)
	}
	before = len(term.String())
	p.key([]byte("\x1b"))
	settled(p)
	result(t, fres)
	if s := term.String()[before:]; pasteModeOf(s) != "off" {
		t.Errorf("the form closed with %q", s)
	}
	p, term, fres, fcancel = formProxy(t, &cols)
	fcancel()
	result(t, fres)
	if s := term.String(); pasteModeOf(s) != "off" {
		t.Errorf("the form interrupted: %q", s)
	}
}

// The shell gone, the terminal gets its mode back, a question open or not.
func TestPasteModeRestoreScreen(t *testing.T) {
	p, out, _, cancel := questionProxy(t)
	defer cancel()
	p.restoreScreen()
	if m := pasteModeOf(out.String()); m != "off" {
		t.Errorf("the mode %q: %q", m, out.String())
	}
}

// At the prompt a paste is readline's whole: a Ctrl+O in it opens no
// viewer and keeps none of the paste from the shell. Typed, it opens the
// viewer.
func TestPastePromptCtrlO(t *testing.T) {
	const paste = "\x1b[200~a\x0fb\r\nc\x1b[201~"
	for cut, reads := range cuts(paste) {
		t.Run(cut, func(t *testing.T) {
			p := keysProxy(t)
			var shell strings.Builder
			for _, r := range reads {
				shell.Write(p.key([]byte(r)))
			}
			if shell.String() != paste || p.view != nil {
				t.Errorf("the shell got %q, the viewer open: %v", shell.String(), p.view != nil)
			}
			if got := p.key([]byte("x\x0f")); string(got) != "x" || p.view == nil {
				t.Errorf("typed: the shell got %q, the viewer open: %v", got, p.view != nil)
			}
		})
	}
}

// Before the first prompt a Ctrl+C pasted is text: the keys stay held
// for readline, and ~/.bashrc goes on. Typed, it lets them go at once; and
// so it does a while after a paste whose end never came.
func TestPasteEarlySignal(t *testing.T) {
	const paste = "\x1b[200~a\x03b\x1a\x1c\x1b[201~"
	for cut, reads := range cuts(paste) {
		t.Run(cut, func(t *testing.T) {
			p := keysProxy(t)
			pty := &terminal{}
			p.holdEarly(pty)
			for _, r := range reads {
				if got := p.key([]byte(r)); len(got) != 0 {
					t.Fatalf("let go: %q", got)
				}
			}
			if p.early == nil {
				t.Fatal("the keys are not held")
			}
			if got := p.key([]byte("\x03")); string(got) != paste+"\x03" {
				t.Errorf("Ctrl+C typed let go %q", got)
			}
		})
	}

	p := keysProxy(t)
	p.holdEarly(&terminal{})
	if got := p.key([]byte("\x1b[200~a")); len(got) != 0 {
		t.Fatalf("let go: %q", got)
	}
	p.seq.shell.last = time.Now().Add(-2 * pasteGap)
	if got := p.key([]byte("\x03")); string(got) != "\x1b[200~a\x03" {
		t.Errorf("after a paste never ended: %q", got)
	}
}
