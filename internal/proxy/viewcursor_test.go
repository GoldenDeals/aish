package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/session"
)

const (
	showCursor = "\x1b[?25h"
	hideCursor = "\x1b[?25l"
)

// cursorShown tells whether the cursor is left visible by s: by the last
// of its sequences that show or hide it, or by none.
func cursorShown(s string) bool {
	return strings.LastIndex(s, showCursor) >= strings.LastIndex(s, hideCursor)
}

// askingProxy is a proxy with the policy's question open, and something
// in the viewer to see.
func askingProxy(t *testing.T) (*Proxy, *terminal, context.CancelFunc, chan error) {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	p.size = func() (int, int) { return 80, 24 }
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() {
		_, err := p.askUser(ctx, "allow?")
		res <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		open := p.ask != nil
		p.mu.Unlock()
		if open {
			return p, out, cancel, res
		}
		if time.Now().After(deadline) {
			t.Fatal("the question never opened")
		}
	}
}

// The viewer opened over the question gives back the cursor as the
// question keeps it: hidden.
func TestViewCursorAsk(t *testing.T) {
	p, out, cancel, res := askingProxy(t)
	defer cancel()
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	before := len(out.String())
	p.key([]byte{ctrlO})
	if p.view != nil || p.ask == nil {
		t.Fatalf("viewer %v, question %v", p.view, p.ask)
	}
	if s := out.String()[before:]; strings.Contains(s, showCursor) || cursorShown(s) {
		t.Errorf("the viewer closed with the cursor shown: %q", s)
	}

	// The question gone while the viewer was open: the cursor comes back.
	p.key([]byte{ctrlO})
	cancel()
	if err := <-res; err == nil {
		t.Error("the question was answered")
	}
	before = len(out.String())
	p.key([]byte{ctrlO})
	if s := out.String()[before:]; !cursorShown(s) {
		t.Errorf("the cursor stayed hidden: %q", s)
	}
}

// So does the viewer opened over the form of ask_user.
func TestViewCursorForm(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxy(t, &cols)
	defer cancel()
	p.mu.Lock()
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.mu.Unlock()
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	before := len(out.String())
	p.key([]byte{ctrlO})
	if p.view != nil || p.form == nil {
		t.Fatalf("viewer %v, form %v", p.view, p.form)
	}
	if s := out.String()[before:]; strings.Contains(s, showCursor) || cursorShown(s) {
		t.Errorf("the viewer closed with the cursor shown: %q", s)
	}
	p.key([]byte(keyEnterSeq + keyEnterSeq))
	if r := result(t, res); r.err != nil || len(r.ans) != 2 {
		t.Errorf("after the viewer: %+v", r)
	}
	if s := out.String(); !cursorShown(s) {
		t.Errorf("the form left the cursor hidden: %q", s)
	}
}

// With neither open the viewer shows the cursor it hid; what it held is
// written after, so the spinner drawn meanwhile hides it again.
func TestViewCursorPrompt(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	p.size = func() (int, int) { return 80, 24 }
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	before := len(out.String())
	p.key([]byte{ctrlO})
	if s := modeless(out.String()[before:]); s != "\x1b[?1049l"+showCursor {
		t.Errorf("the viewer closed with %q", s)
	}

	p.key([]byte{ctrlO})
	p.mu.Lock()
	p.emit([]byte("\r" + hideCursor + "⠋ thinking…"))
	p.mu.Unlock()
	before = len(out.String())
	p.key([]byte{ctrlO})
	if s := out.String()[before:]; !strings.Contains(s, showCursor) || cursorShown(s) {
		t.Errorf("the spinner's cursor: %q", s)
	}
}
