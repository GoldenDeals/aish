// Package inputline is the line the user types in, a pane at the bottom: Enter sends it as a user.message.
// A long line shows its end. A message the bus rolls back comes back to the line if it's still empty.
// Ctrl+C stops the answers still running; with none, it's left to the compositor, which quits.
package inputline

import (
	"context"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/contracts/llm"
	"github.com/GoldenDeals/aish-core/pkg/contracts/tui"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

const (
	z      = 10 // above the conversation for input; the higher z of a status line puts it below
	prompt = "> "
)

type mod struct {
	b      bus.Bus
	ctx    context.Context // ends on Stop, dropping a message still waiting for the bus
	cancel context.CancelFunc

	mu     sync.Mutex // the compositor draws and types while a rolled back message comes back
	handle tui.Handle
	text   string
	w      int                                    // the pane's width at the last Draw; the compositor asks for the cursor right after it
	turns  map[context.Context]context.CancelFunc // the turns of sent messages that aren't over yet
}

func init() { module.Register("tui.inputline", &mod{}) }

func (m *mod) Init(b bus.Bus, _ config.Config) error {
	m.b = b
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.turns = map[context.Context]context.CancelFunc{}
	bus.Async(b, event.ByName(tui.Ready), func(_ context.Context, ev event.Event[tui.Screen]) {
		m.edit(func() { m.handle = ev.Data().Open(tui.Pane, z, m) })
	})
	return nil
}

func (m *mod) Start(context.Context) error { return nil }

func (m *mod) Stop(context.Context) error {
	m.cancel()
	return nil
}

func (m *mod) Size(width, _ int) (int, int) { return width, 1 }

func (m *mod) Draw(scr uv.Screen, area uv.Rectangle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w = area.Dx()
	uv.NewStyledString(prompt+m.visible()).Draw(scr, area)
}

func (m *mod) Cursor() *uv.Cursor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return uv.NewCursor(ansi.StringWidth(prompt+m.visible()), 0)
}

// visible is the end of the text that fits in the pane with the prompt and the cursor after it.
func (m *mod) visible() string {
	return ansi.TruncateLeft(m.text, max(ansi.StringWidth(m.text)-(m.w-len(prompt)-1), 0), "")
}

func (m *mod) Input(ev uv.Event) bool {
	switch ev := ev.(type) {
	case uv.PasteEvent:
		m.edit(func() { m.text += clean(ev.Content) })
	case uv.KeyPressEvent:
		switch {
		case ev.MatchString("enter"):
			m.send()
		case ev.MatchString("ctrl+c"):
			return m.stop()
		case ev.MatchString("backspace"):
			m.edit(func() {
				_, n := utf8.DecodeLastRuneInString(m.text)
				m.text = m.text[:len(m.text)-n]
			})
		case ev.Text != "":
			m.edit(func() { m.text += clean(ev.Text) })
		default:
			return false
		}
	default:
		return false
	}
	return true
}

// send clears the line and dispatches its text apart: the bus waits while an answer streams.
func (m *mod) send() {
	var msg string
	m.edit(func() { msg, m.text = m.text, "" })
	if strings.TrimSpace(msg) == "" {
		return
	}
	ctx, stop := llm.WithTurn(m.ctx)
	turn := llm.Turn(ctx)
	m.mu.Lock()
	m.turns[turn] = stop
	m.mu.Unlock()
	context.AfterFunc(turn, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.turns, turn)
	})
	go func() {
		if err := m.b.Dispatch(ctx, event.New(llm.UserMessage, msg)); err != nil {
			stop() // the provider never got it
			if m.ctx.Err() == nil {
				m.edit(func() {
					if m.text == "" {
						m.text = msg // rolled back: give it back to retry rather than lose it
					}
				})
			}
		}
	}()
}

// stop stops the turns that aren't over and tells whether there were any.
func (m *mod) stop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, stop := range m.turns {
		stop()
	}
	n := len(m.turns)
	clear(m.turns)
	return n > 0
}

// edit changes the line under the lock and asks for a frame.
func (m *mod) edit(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
	if m.handle != nil {
		m.handle.Redraw()
	}
}

// clean keeps the line one line of printable text: a paste may bring newlines, tabs and escape sequences.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}
