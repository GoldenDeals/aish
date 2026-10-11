// Package core is the terminal compositor. It owns the terminal and dispatches tui.Ready with a Screen
// that other modules open elements on (pkg/contracts/tui). On every change it lays the elements out, composes
// them by z and routes input down the z order; it draws nothing of its own. It registers as "tui" and has no config.
package core

import (
	"context"
	"os"
	"slices"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/contracts/tui"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

// interrupt stops the program as ctrl+c would if raw mode didn't swallow it; tests replace it.
var interrupt = func() {
	p, _ := os.FindProcess(os.Getpid()) // never fails on unix
	_ = p.Signal(os.Interrupt)
}

type mod struct {
	b    bus.Bus
	term *uv.Terminal
	wake chan struct{} // holds one pending frame: wakes during a render coalesce into it
	stop context.CancelFunc
	done chan struct{}
	err  error // giving the terminal back, set by the loop before done

	mu    sync.Mutex
	items []*item // bottom to top; copy-on-write, so the loop reads a snapshot without the lock
	texts []*item // Text elements opened since the last frame

	// Only the loop touches these.
	w, h, frameH int
	alt          bool
	queued       []*item // Text elements waiting for the main screen
}

var _ tui.Screen = (*mod)(nil)

func init() { module.Register("tui", &mod{}) }

func (m *mod) Init(b bus.Bus, _ config.Config) error {
	m.b, m.wake = b, make(chan struct{}, 1)
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	m.term = uv.DefaultTerminal()
	if err := m.term.Start(); err != nil {
		return err
	}
	m.term.Screen().HideCursor() // until an element places it
	m.term.Screen().EnableBracketedPaste()
	lctx, stop := context.WithCancel(ctx)
	m.stop, m.done = stop, make(chan struct{})
	go m.loop(lctx)
	if err := m.b.Dispatch(ctx, event.New[tui.Screen](tui.Ready, m)); err != nil {
		_ = m.Stop(ctx) // a failed Start isn't stopped by the runtime: give the terminal back here
		return err
	}
	return nil
}

func (m *mod) Stop(ctx context.Context) error {
	if m.stop == nil {
		return nil
	}
	m.stop()
	select {
	case <-m.done:
		return m.err
	case <-ctx.Done(): // an element blocks the loop: give the terminal back without it
		return m.term.Stop()
	}
}

func (m *mod) Open(k tui.Kind, z int, el uv.Drawable) tui.Handle {
	it := &item{kind: k, z: z, el: el}
	m.mu.Lock()
	if k == tui.Text {
		m.texts = append(m.texts, it)
	} else {
		i := slices.IndexFunc(m.items, func(x *item) bool { return x.z > z }) // after equal z: opened later is above
		if i < 0 {
			i = len(m.items)
		}
		m.items = slices.Insert(slices.Clone(m.items), i, it)
	}
	m.mu.Unlock()
	m.redraw()
	return &handle{m, it}
}

func (m *mod) redraw() {
	select {
	case m.wake <- struct{}{}:
	default: // a frame is already due
	}
}

type handle struct {
	m  *mod
	it *item
}

func (h *handle) Redraw() { h.m.redraw() }

func (h *handle) Close() {
	m, is := h.m, func(x *item) bool { return x == h.it }
	m.mu.Lock()
	m.items = slices.DeleteFunc(slices.Clone(m.items), is)
	m.texts = slices.DeleteFunc(m.texts, is) // a Text closed before its frame is never printed
	m.mu.Unlock()
	m.redraw()
}

func (m *mod) elements() []*item {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.items
}

// loop draws a frame only when something changed. A flood of Redraw needs no frame cap: the blocking
// write to the terminal paces the frames, and the redraws in between coalesce into one.
func (m *mod) loop(ctx context.Context) {
	defer close(m.done)
	for {
		select {
		case <-ctx.Done():
			m.err = m.leave()
			return
		case ev := <-m.term.Events():
			m.event(ev)
		case <-m.wake:
			m.render()
		}
	}
}

// event takes a new terminal size or passes input down the elements, which ask for a frame if it changed them.
func (m *mod) event(ev uv.Event) {
	if ws, ok := ev.(uv.WindowSizeEvent); ok {
		m.w, m.h, m.frameH = ws.Width, ws.Height, -1
		m.redraw()
	} else if k, ok := ev.(uv.KeyPressEvent); !input(m.elements(), ev) && ok && k.MatchString("ctrl+c") {
		interrupt()
	}
}

func (m *mod) render() {
	if m.w == 0 {
		return // no size yet: the terminal sends it first
	}
	scr := m.term.Screen()
	m.mu.Lock()
	items := m.items
	m.queued, m.texts = append(m.queued, m.texts...), nil
	m.mu.Unlock()

	if !m.alt { // print against the frame on screen, before it changes
		for _, t := range m.queued {
			_ = scr.InsertAbove(text(t.el, m.w, m.h, scr.WidthMethod())) // a terminal we can't write to has nothing to show it on
		}
		m.queued = nil
	}
	frameH, alt := layout(items, m.w, m.h)
	if alt != m.alt {
		m.alt, m.frameH = alt, -1
		if alt {
			scr.EnterAltScreen()
		} else {
			scr.ExitAltScreen()
			if len(m.queued) > 0 {
				m.redraw() // print them once the main screen is back
			}
		}
	}
	if frameH != m.frameH {
		scr.Resize(m.w, frameH)
		m.frameH = frameH
	}
	_ = scr.Display(uv.DrawableFunc(func(s uv.Screen, _ uv.Rectangle) {
		setCursor(scr, compose(s, items))
	}))
	// shortcut: uv's Flush leaves its cursor move in the renderer until the next Render, and InsertAbove
	// then starts from the wrong row; render again to send it, drop once uv flushes it itself.
	scr.Render()
	_ = scr.Flush()
}

// leave gives the terminal back with the last inline frame kept and the shell prompt on a new line below it.
func (m *mod) leave() error {
	scr := m.term.Screen()
	if m.alt { // the main screen still shows the frame from before the alt screen: erase it, the prompt goes there
		scr.ExitAltScreen()
		scr.Resize(m.w, 0)
		m.alt, m.frameH = false, 0
	}
	scr.SetCursorPosition(0, max(m.frameH-1, 0))
	scr.ShowCursor() // uv's Reset leaves a hidden cursor hidden
	// The second pass sends the cursor move, see render.
	for range 2 {
		scr.Render()
		_ = scr.Flush()
	}
	if err := m.term.Stop(); err != nil {
		return err
	}
	if !m.alt && m.frameH > 0 {
		_, _ = m.term.Write([]byte("\r\n")) // the frame is already on screen: nothing is lost if this fails
	}
	return nil
}

// setCursor shows c on scr, or hides the cursor when c is nil or hidden.
func setCursor(scr *uv.TerminalScreen, c *uv.Cursor) {
	if c == nil || c.Hidden {
		if scr.CursorVisible() {
			scr.HideCursor()
		}
		return
	}
	scr.SetCursorPosition(c.X, c.Y)
	if shape, blink := scr.CursorStyle(); shape != c.Shape || blink != c.Blink {
		scr.SetCursorStyle(c.Shape, c.Blink)
	}
	if !scr.CursorVisible() {
		scr.ShowCursor()
	}
}
