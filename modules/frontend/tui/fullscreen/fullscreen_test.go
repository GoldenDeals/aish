package fullscreen

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/contracts/llm"
	"github.com/GoldenDeals/aish-core/pkg/contracts/tui"
	"github.com/GoldenDeals/aish-core/pkg/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

// lines is a Block of one row per line, the first prefixed with its kind.
type lines struct{ kind, text string }

func (l *lines) Write(chunk string) { l.text += chunk }

func (l *lines) rows() []string {
	if l.text == "" {
		return nil
	}
	return strings.Split(l.kind+":"+l.text, "\n")
}

func (l *lines) Size(w, _ int) (int, int) { return w, len(l.rows()) }

func (l *lines) Draw(scr uv.Screen, area uv.Rectangle) {
	for i, r := range l.rows() {
		uv.NewStyledString(r).Draw(scr, uv.Rect(area.Min.X, area.Min.Y+i, area.Dx(), 1))
	}
}

func fake(kind string) renderer.Renderer {
	return renderer.Renderer{Kind: kind, New: func(renderer.Pick) renderer.Block { return &lines{kind: kind} }}
}

func screen(m *mod, w, h int) string {
	scr := uv.NewWindow(w, h, nil)
	m.Draw(scr, scr.Bounds())
	return scr.String()
}

func setup(t *testing.T) (*mod, bus.Bus) {
	m, b := &mod{}, corebus.New()
	if err := m.Init(b, nil); err != nil {
		t.Fatal(err)
	}
	return m, b
}

func TestConversation(t *testing.T) {
	m, b := setup(t)
	ctx := context.Background()
	if err := b.Dispatch(ctx, event.New(llm.UserMessage, "lost")); err != nil {
		t.Fatal(err)
	}
	if got := screen(m, 20, 4); got != "\n\n\n" {
		t.Errorf("without renderers: %q", got)
	}
	if err := b.Dispatch(ctx, event.New(renderer.Announce, fake(renderer.Text))); err != nil {
		t.Fatal(err)
	}
	if err := b.Dispatch(ctx, event.New(renderer.Announce, fake(renderer.Text))); err == nil {
		t.Error("a second text renderer was taken")
	}

	ch := make(chan string)
	go func() {
		defer close(ch)
		ch <- "an"
		ch <- "swer"
	}()
	for _, ev := range []event.Any{
		event.New(llm.UserMessage, "hi"),
		event.NewStream(llm.Answer, (<-chan string)(ch)), // no markdown renderer: falls back to text
		event.New(llm.Error, "boom"),
	} {
		if err := b.Dispatch(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	want := "text:> hi\n\ntext:answer\n\ntext:error: boom\n"
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) { // the stream is shown by its own goroutine
		got := screen(m, 20, 6)
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %q, want %q", got, want)
		}
	}

	if err := b.Dispatch(ctx, event.New(renderer.Announce, fake(renderer.Markdown))); err != nil {
		t.Fatal(err)
	}
	if err := b.Dispatch(ctx, event.New(llm.Answer, []string{"md"})); err != nil { // not a stream: shown whole
		t.Fatal(err)
	}
	if got := screen(m, 20, 2); got != "markdown:md\n" {
		t.Errorf("markdown answer: %q", got)
	}
}

func TestRolledBack(t *testing.T) {
	m, b := setup(t)
	ctx := context.Background()
	if err := b.Dispatch(ctx, event.New(renderer.Announce, fake(renderer.Text))); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("fail")
	bus.Sync(b, prio-1, event.ByName(llm.UserMessage), func(context.Context, tx.Tx, event.Event[string]) error { return fail })
	if err := b.Dispatch(ctx, event.New(llm.UserMessage, "hi")); !errors.Is(err, fail) {
		t.Fatalf("dispatch: %v", err)
	}
	if len(m.blocks) != 0 {
		t.Error("the message of a rolled back tx is shown")
	}
}

func TestScroll(t *testing.T) {
	m := &mod{follow: true}
	for i := range 5 {
		m.blocks = append(m.blocks, &lines{kind: string(rune('0' + i)), text: "a\nb\nc"}) // 4 rows with the gap
	}
	if got, want := screen(m, 5, 6), "c\n\n4:a\nb\nc\n"; got != want {
		t.Errorf("follows the end: %q, want %q", got, want)
	}
	pgup, pgdown := uv.KeyPressEvent{Code: uv.KeyPgUp}, uv.KeyPressEvent{Code: uv.KeyPgDown}
	if !m.Input(pgup) {
		t.Fatal("pgup not taken")
	}
	view := "b\nc\n\n3:a\nb\nc"
	if got := screen(m, 5, 6); got != view {
		t.Errorf("a page up: %q, want %q", got, view)
	}
	m.blocks = append(m.blocks, &lines{kind: "5", text: "a"})
	if got := screen(m, 5, 6); got != view {
		t.Errorf("scrolled up, new content moved the view: %q", got)
	}
	m.Input(pgdown)
	m.Input(pgdown)
	if got, want := screen(m, 5, 6), "4:a\nb\nc\n\n5:a\n"; got != want || !m.follow {
		t.Errorf("back at the end: %q follow %v, want %q", got, m.follow, want)
	}
	m.Input(uv.KeyPressEvent{Code: uv.KeyUp})
	m.Input(uv.KeyPressEvent{Code: uv.KeyUp})
	m.Input(uv.KeyPressEvent{Code: uv.KeyDown})
	if got, want := screen(m, 5, 6), "\n4:a\nb\nc\n\n5:a"; got != want || m.follow {
		t.Errorf("a row up: %q follow %v, want %q", got, m.follow, want)
	}
	if !m.Input(uv.MouseWheelEvent{Button: uv.MouseWheelUp}) {
		t.Fatal("wheel not taken")
	}
	if got, want := screen(m, 5, 6), "3:a\nb\nc\n\n4:a\nb"; got != want {
		t.Errorf("a wheel step up: %q, want %q", got, want)
	}
	m.Input(uv.MouseWheelEvent{Button: uv.MouseWheelDown})
	m.Input(uv.MouseWheelEvent{Button: uv.MouseWheelDown})
	if !m.follow {
		t.Error("wheel down didn't get back to the end")
	}
	if m.Input(uv.KeyPressEvent{Code: 'a', Text: "a"}) {
		t.Error("took a key it doesn't scroll by")
	}

	m.top, m.follow, m.rows = anchor{1, 1}, false, 2
	m.move([]int{2, 6, 2}, offset([]int{2, 6, 2}, m.top)) // heights after a resize
	if m.top != (anchor{1, 1}) {
		t.Errorf("a resize moved the view to %v", m.top)
	}
}

// clipboard is a Screen that records what's copied.
type clipboard []string

func (c *clipboard) Open(tui.Kind, int, uv.Drawable) tui.Handle { return nil }
func (c *clipboard) Copy(text string)                           { *c = append(*c, text) }

func TestSelect(t *testing.T) {
	var clip clipboard
	m := &mod{follow: true, scr: &clip}
	m.blocks = []renderer.Block{&lines{kind: "0", text: "hello\nworld"}, &lines{kind: "1", text: "next"}}
	screen(m, 10, 6) // rows "0:hello", "world", "", "1:next", ""
	left := uv.MouseLeft

	if !m.Input(uv.MouseClickEvent{X: 1, Y: 3, Button: left}) {
		t.Fatal("press not taken")
	}
	scr := uv.NewWindow(10, 6, nil)
	rev := func(x, y int) bool { return scr.CellAt(x, y).Style.Attrs&uv.AttrReverse != 0 }
	m.Draw(scr, scr.Bounds())
	if rev(1, 3) {
		t.Error("a press alone highlights")
	}
	m.Input(uv.MouseMotionEvent{X: 2, Y: 0, Button: left}) // upwards: the selection runs from here to the press
	scr = uv.NewWindow(10, 6, nil)
	m.Draw(scr, scr.Bounds())
	if rev(1, 0) || !rev(2, 0) || !rev(1, 3) || rev(2, 3) {
		t.Error("highlight off the selection")
	}
	m.Input(uv.MouseReleaseEvent{X: 2, Y: 0, Button: left})
	if want := []string{"hello\nworld\n\n1:"}; !slices.Equal(clip, want) {
		t.Errorf("copied %q, want %q", clip, want)
	}
	scr = uv.NewWindow(10, 6, nil)
	m.Draw(scr, scr.Bounds())
	if rev(2, 0) {
		t.Error("the selection stays after the release")
	}

	m.Input(uv.MouseClickEvent{X: 0, Y: 0, Button: left})
	m.Input(uv.MouseReleaseEvent{X: 0, Y: 0, Button: left})
	if len(clip) != 1 {
		t.Errorf("a click copied: %q", clip)
	}

	for range 2 { // on "0:hello": the colon ends the word
		m.Input(uv.MouseClickEvent{X: 4, Y: 0, Button: left})
		m.Input(uv.MouseReleaseEvent{X: 4, Y: 0, Button: left})
	}
	if len(clip) != 2 || clip[1] != "hello" {
		t.Errorf("a double click copied %q, want hello", clip[1:])
	}
	for range 3 { // on "1:next", elsewhere: counts from one again
		m.Input(uv.MouseClickEvent{X: 3, Y: 3, Button: left})
		m.Input(uv.MouseReleaseEvent{X: 3, Y: 3, Button: left})
	}
	if want := []string{"next", "1:next"}; !slices.Equal(clip[2:], want) {
		t.Errorf("a triple click copied %q, want %q", clip[2:], want)
	}
	for deadline := time.Now().Add(time.Second); ; time.Sleep(10 * time.Millisecond) {
		m.mu.Lock()
		on := m.sel.on
		m.mu.Unlock()
		if !on {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the double-clicked word stays highlighted")
		}
	}
	if m.Input(uv.MouseClickEvent{X: 0, Y: 6, Button: left}) || m.Input(uv.MouseMotionEvent{X: 0, Y: 0, Button: left}) {
		t.Error("took a press on a pane below, or the drag from it")
	}
	if m.Input(uv.MouseClickEvent{X: 0, Y: 0, Button: uv.MouseMiddle}) {
		t.Error("took the middle button, the compositor's paste")
	}
}
