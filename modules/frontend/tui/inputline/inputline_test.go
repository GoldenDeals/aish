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

func line(m *mod, w int) (string, int) {
	scr := uv.NewWindow(w, 1, nil)
	m.Draw(scr, scr.Bounds())
	return scr.String(), m.Cursor().X
}

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
	m.Input(uv.KeyPressEvent{Code: uv.KeyBackspace}) // a whole two-byte rune
	m.Input(uv.PasteEvent{Content: "x\ny\x1b[2Jz"})  // one line, no escape sequences
	if got, x := line(m, 20); got != "> прx y[2Jz" || x != 11 {
		t.Errorf("got %q cursor %d", got, x)
	}
	if got, x := line(m, 8); got != "> y[2Jz" || x != 7 { // a long line shows its end, the cursor fits
		t.Errorf("narrow: %q cursor %d", got, x)
	}
	if m.Input(uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl}) {
		t.Error("took ctrl+c")
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
	if s, _ := line(m, 20); s != ">" {
		t.Errorf("not cleared: %q", s)
	}

	typed(m, "roll back")
	m.Input(enter)
	<-got
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if s, _ := line(m, 20); s == "> roll back" {
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
