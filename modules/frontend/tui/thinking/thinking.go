// Package thinking marks where the model writes: a cursor after the conversation's last character, from
// a user.message until its turn is over (llm.WithTurn). It moves with the answer as it streams; when the model
// says nothing for a second, a spinner with "thinking…" stands in its place until the next chunk.
// It draws over a conversation that is a caret.Source, tui.fullscreen; without one it shows nothing.
package thinking

import (
	"context"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tui"
	"github.com/GoldenDeals/aish-core/contracts/tui/caret"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

const (
	z      = 1 // right above the conversation, in its area
	prio   = 0 // above the provider, which waits for the answer's end
	step   = 80 * time.Millisecond
	cursor = "█"
	label  = " thinking…"
)

var (
	pause  = time.Second // the silence after which the model is thinking
	frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

type mod struct {
	mu      sync.Mutex // the compositor draws while turns begin and end, chunks come and timers fire
	scr     tui.Screen
	src     caret.Source
	handle  tui.Handle
	turns   int       // turns of user messages that aren't over yet
	heard   time.Time // the turn's start or its last chunk
	timer   *time.Timer
	quiet   bool // silent for pause: thinking
	frame   int
	ticking bool // a spinner tick is scheduled
}

func init() { module.Register("tui.thinking", &mod{}) }

func (m *mod) Init(b bus.Bus, _ config.Config) error {
	bus.Async(b, event.ByName(tui.Ready), func(_ context.Context, ev event.Event[tui.Screen]) {
		m.edit(func() { m.scr = ev.Data() })
	})
	bus.Async(b, event.ByName(caret.Announce), func(_ context.Context, ev event.Event[caret.Source]) {
		m.edit(func() { m.src = ev.Data() })
	})
	bus.Async(b, event.ByName(llm.UserMessage), func(ctx context.Context, _ event.Event[string]) {
		turn := llm.Turn(ctx)
		if turn.Done() == nil {
			return // shortcut: a message sent without llm.WithTurn has no end to wait for; follow llm.answer if such senders appear
		}
		m.edit(func() {
			m.turns++
			m.hear()
		})
		context.AfterFunc(turn, func() { m.edit(func() { m.turns-- }) })
	})
	bus.Sync(b, prio, event.ByName(llm.Answer), func(ctx context.Context, _ tx.Tx, ev event.Event[[]string]) error {
		if s, ok := ev.(event.Stream[string]); ok {
			go func() { // the tx waits for the stream's end: hear it as it comes, past the tx's ctx
				for range s.Read(context.WithoutCancel(ctx)) {
					m.edit(m.hear)
				}
			}()
		}
		return nil
	})
	return nil
}

func (m *mod) Start(context.Context) error { return nil }
func (m *mod) Stop(context.Context) error  { return nil }

// hear notes that the model spoke: the cursor again, and thinking after pause.
func (m *mod) hear() {
	m.heard, m.quiet = time.Now(), false
	if m.timer == nil {
		m.timer = time.AfterFunc(pause, m.silence)
	} else {
		m.timer.Reset(pause)
	}
}

func (m *mod) silence() {
	m.edit(func() {
		m.quiet = m.turns > 0 && time.Since(m.heard) >= pause // a chunk may have come while the timer fired
		if m.quiet && !m.ticking {
			m.ticking = true
			time.AfterFunc(step, m.tick)
		}
	})
}

// tick moves the spinner on while the model is thinking.
func (m *mod) tick() {
	m.edit(func() {
		m.frame++
		if m.ticking = m.quiet && m.turns > 0; m.ticking {
			time.AfterFunc(step, m.tick)
		}
	})
}

// Draw puts the cursor after the last character, or on the next row when it doesn't fit there.
func (m *mod) Draw(scr uv.Screen, area uv.Rectangle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.turns == 0 {
		return
	}
	p, ok := m.src.Last() // the conversation is drawn first, below: this frame's
	if !ok {
		return
	}
	s := cursor
	if m.quiet {
		s = " " + frames[m.frame%len(frames)] + label
	}
	w := ansi.StringWidth(s)
	x, y := p.X+1, p.Y
	if x > 0 && x+w > area.Dx() {
		x, y = 0, y+1
	}
	uv.NewStyledString(s).Draw(scr, uv.Rect(area.Min.X+x, area.Min.Y+y, w, 1))
}

// edit changes the state under the lock and asks for a frame. The element opens once there is both
// the screen and the conversation: a Fullscreen element alone would take the terminal to the alt screen.
func (m *mod) edit(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
	if m.handle == nil && m.scr != nil && m.src != nil {
		m.handle = m.scr.Open(tui.Fullscreen, z, m)
	}
	if m.handle != nil {
		m.handle.Redraw()
	}
}
