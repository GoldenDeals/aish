package inputline

import (
	"context"
	"errors"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/contracts/llm"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

func typed(m *mod, s string) {
	for _, r := range s {
		m.Input(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
}

// view draws m on a pane w wide and h high, as many rows as it asks for if h is 0.
func view(m *mod, w, h int) (string, uv.Position) {
	if h == 0 {
		_, h = m.Size(w, 100)
	}
	scr := uv.NewWindow(w, h, nil)
	m.Draw(scr, scr.Bounds())
	return scr.String(), m.Cursor().Position
}

func key(code rune, mod uv.KeyMod) uv.KeyPressEvent { return uv.KeyPressEvent{Code: code, Mod: mod} }

func setup(t *testing.T) (*mod, bus.Bus) {
	m, b := &mod{}, corebus.New()
	if err := m.Init(b, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m, b
}

func TestEdit(t *testing.T) {
	m, _ := setup(t)
	typed(m, "при")
	m.Input(uv.KeyPressEvent{Code: uv.KeyBackspace})      // a whole two-byte rune
	m.Input(uv.PasteEvent{Content: "x\ry\r\n\x1b[2Jz\t"}) // newlines as terminals paste them, no escape sequences
	if m.text != "прx\ny\n[2Jz\t" {
		t.Errorf("text %q", m.text)
	}
	if got, c := view(m, 20, 0); got != "> прx\n  y\n  [2Jz" || c != uv.Pos(7, 2) { // String trims the tab's space
		t.Errorf("got %q cursor %v", got, c)
	}
	if got, c := view(m, 6, 0); got != "> прx\n  y\n  [2J\n  z" || c != uv.Pos(4, 3) { // wrapped, a column left for the cursor
		t.Errorf("narrow: %q cursor %v", got, c)
	}
	if got, c := view(m, 6, 2); got != "  [2J\n  z" || c != uv.Pos(4, 1) { // a lower pane keeps the cursor's row
		t.Errorf("low: %q cursor %v", got, c)
	}
	if m.Input(uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl}) {
		t.Error("took ctrl+c")
	}
}

func TestMove(t *testing.T) {
	m, _ := setup(t)
	m.Input(uv.PasteEvent{Content: "one two\nthree"})
	for _, step := range []struct {
		k   uv.KeyPressEvent
		pos int
	}{
		{key('a', uv.ModCtrl), 8}, // the line's start
		{key(uv.KeyLeft, 0), 7},
		{key('b', uv.ModCtrl), 6},
		{key('b', uv.ModAlt), 4}, // the word's start
		{key(uv.KeyLeft, uv.ModCtrl), 0},
		{key('f', uv.ModAlt), 3}, // the word's end
		{key(uv.KeyRight, uv.ModCtrl), 7},
		{key(uv.KeyRight, 0), 8},
		{key('f', uv.ModCtrl), 9},
		{key(uv.KeyEnd, 0), 13},
		{key(uv.KeyHome, 0), 8},
		{key('e', uv.ModCtrl), 13},
	} {
		if !m.Input(step.k) || m.pos != step.pos {
			t.Fatalf("%v: at %d, want %d", step.k, m.pos, step.pos)
		}
	}
	m.Input(key(uv.KeyHome, 0))
	typed(m, "e\u0301") // an accent the terminal sends apart
	m.Input(key(uv.KeyLeft, 0))
	if m.pos != 8 {
		t.Errorf("stopped inside a grapheme: %d", m.pos)
	}
	m.Input(key(uv.KeyRight, 0))
	m.Input(key(uv.KeyBackspace, 0))
	if got, c := view(m, 20, 0); got != "> one two\n  three" || c != uv.Pos(2, 1) {
		t.Errorf("backspace: %q cursor %v", got, c)
	}
}

func TestKill(t *testing.T) {
	m, _ := setup(t)
	m.Input(uv.PasteEvent{Content: "one two-three\nfour five"})
	for _, step := range []struct {
		k    uv.KeyPressEvent
		text string
		pos  int
	}{
		{key('w', uv.ModCtrl), "one two-three\nfour ", 19},
		{key('a', uv.ModCtrl), "one two-three\nfour ", 14},
		{key(uv.KeyLeft, 0), "one two-three\nfour ", 13},
		{key('w', uv.ModCtrl), "one \nfour ", 4}, // to the space: two-three is one word to ctrl+w
		{key('a', uv.ModCtrl), "one \nfour ", 0},
		{key('d', uv.ModAlt), " \nfour ", 0},
		{key(uv.KeyDelete, 0), "\nfour ", 0},
		{key(uv.KeyDelete, 0), "four ", 0},
		{key(uv.KeyRight, 0), "four ", 1},
		{key('k', uv.ModCtrl), "f", 1},
		{key('u', uv.ModCtrl), "", 0},
	} {
		if !m.Input(step.k) || m.text != step.text || m.pos != step.pos {
			t.Fatalf("%v: %q at %d, want %q at %d", step.k, m.text, m.pos, step.text, step.pos)
		}
	}
}

func TestSend(t *testing.T) {
	m, b := setup(t)
	got := make(chan string, 2)
	fail := errors.New("fail")
	bus.Sync(b, 0, event.ByName(llm.UserMessage), func(_ context.Context, _ tx.Tx, ev event.Event[string]) error {
		got <- ev.Data()
		if ev.Data() == "roll back" {
			return fail
		}
		return nil
	})
	enter := uv.KeyPressEvent{Code: uv.KeyEnter}

	typed(m, "  ")
	m.Input(enter) // blank: not sent
	typed(m, "hi")
	m.Input(enter)
	if msg := <-got; msg != "hi" {
		t.Errorf("sent %q", msg)
	}
	if s, _ := view(m, 20, 0); s != ">" {
		t.Errorf("not cleared: %q", s)
	}

	typed(m, `a\`)
	m.Input(enter) // continues the line, as in bash
	typed(m, "b")
	m.Input(key(uv.KeyEnter, uv.ModShift))
	typed(m, "c")
	m.Input(enter)
	if msg := <-got; msg != "a\nb\nc" {
		t.Errorf("sent %q", msg)
	}

	typed(m, "roll back")
	m.Input(enter)
	<-got
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if s, _ := view(m, 20, 0); s == "> roll back" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a rolled back message is lost")
		}
	}
}

func TestStop(t *testing.T) {
	m, b := setup(t)
	turns := make(chan context.Context, 1)
	bus.Sync(b, 0, event.ByName(llm.UserMessage), func(ctx context.Context, _ tx.Tx, ev event.Event[string]) error {
		turns <- ctx
		if ev.Data() == "roll back" {
			return errors.New("fail")
		}
		return nil
	})
	ctrlC := uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl}
	send := func(s string) context.Context {
		typed(m, s)
		m.Input(uv.KeyPressEvent{Code: uv.KeyEnter})
		return <-turns
	}
	idle := func() {
		t.Helper()
		for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
			m.mu.Lock()
			n := len(m.turns)
			m.mu.Unlock()
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("a turn is still running")
			}
		}
	}

	ctx := send("stop it")
	if !m.Input(ctrlC) || llm.Turn(ctx).Err() == nil {
		t.Error("ctrl+c didn't stop the answer")
	}
	if m.Input(ctrlC) {
		t.Error("took ctrl+c with no answer running")
	}

	llm.End(send("answered")) // as the provider does
	idle()
	send("roll back")
	idle()
	if m.Input(ctrlC) {
		t.Error("took ctrl+c after the turns were over")
	}
}
