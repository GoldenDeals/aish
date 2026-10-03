package proxy

import (
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/session"
)

// pasted is a bracketed paste holding what a reader of keys would act on:
// y answers a question, q leaves the viewer, Enter and a digit answer the
// form, Ctrl+C stops the request, and a newline would run a line.
const pasted = "\x1b[200~ssh -o BatchMode=yes h 'echo q' | head -60\r\nls 1\x03\x1b[201~"

// cuts are the reads a paste may come in.
func cuts(s string) map[string][]string {
	bytewise := make([]string, len(s))
	for i := range s {
		bytewise[i] = s[i : i+1]
	}
	return map[string][]string{
		"whole":         {s},
		"after ESC":     {s[:1], s[1:]},
		"in the CSI":    {s[:3], s[3:]},
		"end after ESC": {s[:len(s)-5], s[len(s)-5:]},
		"byte by byte":  bytewise,
	}
}

// settled waits for the keys held for a cut sequence to be taken for what
// they are.
func settled(p *Proxy) {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		p.mu.Lock()
		held := p.seq.part != nil
		p.mu.Unlock()
		if !held {
			return
		}
	}
}

func keysProxy(t *testing.T) *Proxy {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = &terminal{}
	p.size = func() (int, int) { return 80, 24 }
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	return p
}

// TestPasteBrackets: a bracketed paste, cut by the reads anywhere, reaches
// the shell whole or not at all, whoever reads the keys: never [200~
// without its ESC, never the text as keys of the viewer, the question, the
// form or the panes, and never what follows such a key on to the shell.
func TestPasteBrackets(t *testing.T) {
	defer func(w time.Duration) { escWait = w }(escWait)
	escWait = 10 * time.Millisecond

	// A state opens p and judges it, given what key gave the shell, under
	// p.mu; done ends what it started.
	type state func(t *testing.T) (p *Proxy, check func(t *testing.T, shell string), done func())
	none := func() {}
	for _, st := range []struct {
		name string
		open state
	}{
		{"prompt", func(t *testing.T) (*Proxy, func(*testing.T, string), func()) {
			return keysProxy(t), func(t *testing.T, shell string) {
				if shell != pasted {
					t.Errorf("the shell got %q", shell)
				}
			}, none
		}},
		{"viewer", func(t *testing.T) (*Proxy, func(*testing.T, string), func()) {
			p := keysProxy(t)
			p.key([]byte{ctrlO})
			return p, func(t *testing.T, shell string) {
				if shell != "" || p.view == nil {
					t.Errorf("the shell got %q, the viewer open: %v", shell, p.view != nil)
				}
			}, none
		}},
		{"question", func(t *testing.T) (*Proxy, func(*testing.T, string), func()) {
			p, _, cancel, _ := askingProxy(t)
			return p, func(t *testing.T, shell string) {
				if shell != "" || p.ask == nil {
					t.Errorf("the shell got %q, the question open: %v", shell, p.ask != nil)
				}
			}, cancel
		}},
		{"viewer over the question", func(t *testing.T) (*Proxy, func(*testing.T, string), func()) {
			p, _, cancel, _ := askingProxy(t)
			p.key([]byte{ctrlO})
			return p, func(t *testing.T, shell string) {
				if shell != "" || p.view == nil || p.ask == nil {
					t.Errorf("the shell got %q, the viewer open: %v, the question: %v", shell, p.view != nil, p.ask != nil)
				}
			}, cancel
		}},
		{"form", func(t *testing.T) (*Proxy, func(*testing.T, string), func()) {
			cols := 80
			p, _, _, cancel := formProxy(t, &cols)
			return p, func(t *testing.T, shell string) {
				if shell != "" || p.form == nil {
					t.Fatalf("the shell got %q, the form open: %v", shell, p.form != nil)
				}
				if f := p.form.f; f.step != 0 || f.cur[0] != 0 || f.other[0] != "" {
					t.Errorf("the paste was keys of the form: step %d, cursor %d, other %q", f.step, f.cur[0], f.other[0])
				}
			}, cancel
		}},
		{"panes", func(t *testing.T) (*Proxy, func(*testing.T, string), func()) {
			p, _, u, _ := paneProxy(t)
			u.Pane("one")
			u.Pane("two")
			due(p)
			return p, func(t *testing.T, shell string) {
				if shell != "" || !p.panes.shown || p.panes.zoom != -1 {
					t.Errorf("the shell got %q, the panes shown: %v, zoom %d", shell, p.panes.shown, p.panes.zoom)
				}
			}, none
		}},
		{"before the first prompt", func(t *testing.T) (*Proxy, func(*testing.T, string), func()) {
			p := keysProxy(t)
			pty := &terminal{}
			p.holdEarly(pty)
			return p, func(t *testing.T, shell string) {
				// Ctrl+C lets the keys go at once, as typed; else readline
				// gets them.
				if shell == "" {
					p.earlyPrompt()
					p.earlyOutput(pasteOn)
				}
				if got := shell + pty.String(); got != pasted || shell != "" && pty.String() != "" {
					t.Errorf("the shell got %q at once and %q from readline", shell, pty.String())
				}
			}, none
		}},
	} {
		for cut, reads := range cuts(pasted) {
			t.Run(st.name+"/"+cut, func(t *testing.T) {
				p, check, done := st.open(t)
				defer done()
				var shell strings.Builder
				for _, r := range reads {
					shell.Write(p.key([]byte(r)))
				}
				settled(p)
				got := shell.String()
				if strings.Contains(strings.ReplaceAll(got, "\x1b[", ""), "[20") {
					t.Errorf("a bracket without its ESC: %q", got)
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				check(t, got)
			})
		}
	}
}

// Esc alone still is Esc to a reader of keys once nothing follows it, and
// the keys around a paste are keys. At the prompt the keys go at once.
func TestPasteEsc(t *testing.T) {
	defer func(w time.Duration) { escWait = w }(escWait)
	escWait = 10 * time.Millisecond

	p := keysProxy(t)
	p.key([]byte{ctrlO})
	if got := p.key([]byte("\x1b")); got != nil {
		t.Errorf("Esc went to the shell: %q", got)
	}
	settled(p)
	if p.view != nil {
		t.Error("Esc left the viewer open")
	}
	if got := p.key([]byte("ls\x1b")); string(got) != "ls\x1b" {
		t.Errorf("at the prompt: %q", got)
	}

	cols := 80
	p, _, res, cancel := formProxy(t, &cols)
	defer cancel()
	if got := p.key([]byte(keyDownSeq + pasted + keyEnterSeq)); got != nil {
		t.Errorf("went to the shell: %q", got)
	}
	p.mu.Lock()
	if f := p.form.f; f.step != 1 || f.chosen[0] != 1 {
		t.Errorf("the keys around the paste: step %d, chosen %d", f.step, f.chosen[0])
	}
	p.mu.Unlock()
	p.key([]byte("\x1b"))
	if r := result(t, res); r.ans != nil || r.err != nil {
		t.Errorf("Esc: %+v", r)
	}
}

// A paste into the form's Other is the text typed there, on one line; it
// answers nothing until Enter.
func TestPasteFormOther(t *testing.T) {
	defer func(w time.Duration) { escWait = w }(escWait)
	escWait = 10 * time.Millisecond

	cols := 80
	p, _, _, cancel := formProxy(t, &cols)
	defer cancel()
	p.key([]byte(keyDownSeq + keyDownSeq + "my "))
	for _, r := range cuts("\x1b[200~src/a b\r\nc\t1\x03\x1b[201~")["after ESC"] {
		if got := p.key([]byte(r)); got != nil {
			t.Errorf("went to the shell: %q", got)
		}
	}
	settled(p)
	p.mu.Lock()
	if f := p.form.f; f.step != 0 || f.other[0] != "my src/a b c 1" {
		t.Errorf("step %d, other %q", f.step, f.other[0])
	}
	p.mu.Unlock()
	p.key([]byte(keyEnterSeq))
	p.mu.Lock()
	if f := p.form.f; f.step != 1 {
		t.Errorf("Enter after the paste: step %d", f.step)
	}
	p.mu.Unlock()
}
