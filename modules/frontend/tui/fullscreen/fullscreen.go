// Package fullscreen shows the conversation on the whole screen, above the panes: user messages and errors
// as text, answers as markdown, each drawn by the renderer of its kind (pkg/contracts/tui/renderer).
// It follows the end while new content comes; PgUp and PgDn scroll it. Build it with tui.renderer.plaintext:
// content of a kind without a renderer falls back to it, and without it such content isn't shown.
package fullscreen

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/contracts/llm"
	"github.com/GoldenDeals/aish-core/pkg/contracts/tui"
	"github.com/GoldenDeals/aish-core/pkg/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

const (
	z    = 0 // under every pane and overlay
	prio = 0 // above the provider, which waits for the answer's end before it records it
	gap  = 1 // blank rows after each block
)

type mod struct {
	rmu       sync.Mutex // pick runs in blocks' Write too, under mu
	renderers map[string]renderer.Renderer

	mu      sync.Mutex // the compositor draws while bus handlers and stream readers add
	handle  tui.Handle
	blocks  []renderer.Block
	follow  bool   // keep the end in view
	top     anchor // the first row in view
	w, rows int    // the view at the last frame, for scrolling
}

// anchor is the row-th row of the block-th block. Kept by block, the view stays on its content
// when a resize changes the blocks' heights.
type anchor struct{ block, row int }

func init() { module.Register("tui.fullscreen", &mod{}) }

func (m *mod) Init(b bus.Bus, _ config.Config) error {
	m.renderers, m.follow = map[string]renderer.Renderer{}, true
	bus.Async(b, event.ByName(tui.Ready), func(_ context.Context, ev event.Event[tui.Screen]) {
		m.edit(func() { m.handle = ev.Data().Open(tui.Fullscreen, z, m) })
	})
	bus.Sync(b, prio, event.ByName(renderer.Announce), m.announce)
	bus.Sync(b, prio, event.ByName(llm.UserMessage), func(_ context.Context, t tx.Tx, ev event.Event[string]) error {
		m.add(t, renderer.Text, "> "+ev.Data())
		return nil
	})
	bus.Sync(b, prio, event.ByName(llm.Error), func(_ context.Context, t tx.Tx, ev event.Event[string]) error {
		m.add(t, renderer.Text, "error: "+ev.Data())
		return nil
	})
	bus.Sync(b, prio, event.ByName(llm.Answer), m.answer)
	return nil
}

func (m *mod) Start(context.Context) error { return nil }
func (m *mod) Stop(context.Context) error  { return nil }

func (m *mod) announce(_ context.Context, t tx.Tx, ev event.Event[renderer.Renderer]) error {
	r := ev.Data()
	m.rmu.Lock()
	defer m.rmu.Unlock()
	if _, ok := m.renderers[r.Kind]; ok {
		return fmt.Errorf("render: a second renderer of %q", r.Kind)
	}
	m.renderers[r.Kind] = r
	t.OnAbort(func() {
		m.rmu.Lock()
		delete(m.renderers, r.Kind)
		m.rmu.Unlock()
	})
	return nil
}

func (m *mod) pick(kind string) renderer.Block {
	m.rmu.Lock()
	r, ok := m.renderers[kind]
	if !ok {
		r, ok = m.renderers[renderer.Text]
	}
	m.rmu.Unlock()
	if !ok {
		return nil
	}
	return r.New(m.pick)
}

// add appends a Block of kind with text, for as long as t isn't rolled back; nil if nothing renders it.
func (m *mod) add(t tx.Tx, kind, text string) renderer.Block {
	b := m.pick(kind)
	if b == nil {
		return nil
	}
	m.edit(func() {
		b.Write(text)
		m.blocks = append(m.blocks, b)
	})
	t.OnAbort(func() {
		m.edit(func() { m.blocks = slices.DeleteFunc(m.blocks, func(x renderer.Block) bool { return x == b }) })
	})
	return b
}

func (m *mod) answer(ctx context.Context, t tx.Tx, ev event.Event[[]string]) error {
	s, ok := ev.(event.Stream[string])
	if !ok {
		m.add(t, renderer.Markdown, strings.Join(ev.Data(), ""))
		return nil
	}
	b := m.add(t, renderer.Markdown, "")
	if b == nil {
		return nil
	}
	go func() { // the tx waits for the stream's end: show it as it comes, past the tx's ctx
		for chunk := range s.Read(context.WithoutCancel(ctx)) {
			m.edit(func() { b.Write(chunk) })
		}
	}()
	return nil
}

// edit changes the transcript under the lock and asks for a frame.
func (m *mod) edit(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
	if m.handle != nil {
		m.handle.Redraw()
	}
}

func (m *mod) Draw(scr uv.Screen, area uv.Rectangle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w, m.rows = area.Dx(), area.Dy()
	hs := m.heights()
	pos := offset(hs, m.top)
	if m.follow {
		pos = math.MaxInt
	}
	m.move(hs, pos)
	y := area.Min.Y - m.top.row
	for i := m.top.block; i < len(m.blocks) && y < area.Max.Y; i++ {
		m.blocks[i].Draw(scr, uv.Rect(area.Min.X, y, m.w, hs[i]-gap))
		y += hs[i]
	}
}

func (m *mod) Input(ev uv.Event) bool {
	k, ok := ev.(uv.KeyPressEvent)
	if !ok {
		return false
	}
	var n int
	switch {
	case k.MatchString("pgup"):
		n = -1
	case k.MatchString("pgdown"):
		n = 1
	default:
		return false
	}
	m.edit(func() {
		hs := m.heights()
		m.move(hs, offset(hs, m.top)+n*max(m.rows-1, 1))
	})
	return true
}

// heights are the blocks' rows at the last width, gaps included.
func (m *mod) heights() []int {
	hs := make([]int, len(m.blocks))
	for i, b := range m.blocks {
		_, h := b.Size(m.w, math.MaxInt)
		hs[i] = h + gap
	}
	return hs
}

// move puts the view's first row at pos, kept within the content; at the end the view follows it.
func (m *mod) move(hs []int, pos int) {
	total := 0
	for _, h := range hs {
		total += h
	}
	end := max(total-m.rows, 0)
	pos = min(max(pos, 0), end)
	m.follow = pos == end
	m.top = anchor{}
	for m.top.block < len(hs) && pos >= hs[m.top.block] {
		pos -= hs[m.top.block]
		m.top.block++
	}
	m.top.row = pos
}

// offset is a's row counted from the transcript's start.
func offset(hs []int, a anchor) int {
	pos := a.row
	for _, h := range hs[:min(a.block, len(hs))] {
		pos += h
	}
	return pos
}
