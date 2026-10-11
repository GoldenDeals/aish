package thinking

import (
	"context"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tui/caret"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
)

// at is a conversation whose last character is at p; only the test goroutine draws, so no lock.
type at struct{ p uv.Position }

func (a *at) Last() (uv.Position, bool) { return a.p, true }

// view draws "abc" as the conversation, then m over it.
func view(m *mod, w int) string {
	scr := uv.NewWindow(w, 2, nil)
	uv.NewStyledString("abc").Draw(scr, uv.Rect(0, 0, 3, 1))
	m.Draw(scr, scr.Bounds())
	return scr.String()
}

// wait polls view until ok; the bus delivers and the timers fire in their own goroutines.
func wait(t *testing.T, m *mod, what string, ok func(string) bool) string {
	t.Helper()
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if v := view(m, 20); ok(v) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: view %q", what, view(m, 20))
		}
	}
}

func TestTurn(t *testing.T) {
	pause = 100 * time.Millisecond // not put back: timers left from the turn still read it
	m, b, src := &mod{}, corebus.New(), &at{uv.Pos(2, 0)}
	if err := m.Init(b, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Dispatch(ctx, event.New(caret.Announce, caret.Source(src))); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) { // Draw runs only once there is a Source
		m.mu.Lock()
		got := m.src != nil
		m.mu.Unlock()
		if got {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no source")
		}
	}
	if v := view(m, 20); v != "abc\n" {
		t.Fatalf("idle: %q", v)
	}

	turn, stop := llm.WithTurn(ctx)
	if err := b.Dispatch(turn, event.New(llm.UserMessage, "hi")); err != nil {
		t.Fatal(err)
	}
	wait(t, m, "the cursor after the last character", func(v string) bool { return v == "abc█\n" })
	thinking := func(v string) bool { return strings.HasPrefix(v, "abc ") && strings.HasSuffix(v, label+"\n") }
	first := wait(t, m, "thinking after a pause", thinking)
	wait(t, m, "the spinner moves", func(v string) bool { return thinking(v) && v != first })

	ch := make(chan string)
	go func() { _ = b.Dispatch(ctx, event.NewStream(llm.Answer, (<-chan string)(ch))) }()
	ch <- "x"
	wait(t, m, "the cursor again on a chunk", func(v string) bool { return v == "abc█\n" })
	wait(t, m, "thinking again", thinking)
	src.p = uv.Pos(10, 0)
	wait(t, m, "thinking on the next row when it doesn't fit", func(v string) bool {
		return strings.HasPrefix(v, "abc\n ") && strings.HasSuffix(v, label)
	})
	src.p = uv.Pos(-1, 1)
	wait(t, m, "an empty last block: from its first column", func(v string) bool {
		return strings.HasPrefix(v, "abc\n ") && strings.HasSuffix(v, label)
	})
	close(ch)

	stop()
	wait(t, m, "the turn is over", func(v string) bool { return v == "abc\n" })
}
