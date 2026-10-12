// Package fullscreen shows the conversation on the whole screen, above the panes: user messages and errors
// as text, answers as markdown, tool calls with their results as tools, as the renderer core draws content
// of that kind (contracts/tui/renderer).
// It follows the end while new content comes; PgUp and PgDn scroll it by a page, Up and Down by a row,
// the mouse wheel by three. A drag with the left button selects; the release copies and drops the selection.
// A double click copies the word under it, a triple click the row.
// It tells where its text ends as a caret.Source, for a module that marks where the model writes.
// Build it with the renderer core (frontend/tui/renderer): without it, nothing is shown.
package fullscreen

import (
	"context"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/contracts/tui"
	"github.com/GoldenDeals/aish-core/contracts/tui/caret"
	"github.com/GoldenDeals/aish-core/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

const (
	z     = 0 // under every pane and overlay
	prio  = 0 // above the provider, which waits for the answer's end before it records it
	gap   = 1 // blank rows after each block
	wheel = 3 // rows a wheel step scrolls, as terminals scroll their scrollback

	doubleClick = 400 * time.Millisecond // a click on the cell this soon after the last one counts with it
	flash       = 300 * time.Millisecond // how long what a double or triple click grabs stays highlighted, as in tmux
	separators  = ",│`|:\"' ()[]{}<>\t"  // what ends a double-clicked word: Alacritty's default
)

type mod struct {
	b bus.Bus

	mu      sync.Mutex // the compositor draws while bus handlers and stream readers add
	scr     tui.Screen
	handle  tui.Handle
	blocks  []renderer.Block
	calls   map[string]renderer.Block // tool calls waiting for their results; only bus handlers touch it
	follow  bool   // keep the end in view
	top     anchor // the first row in view
	w, rows int    // the view at the last frame, for scrolling
	sel     selection
	last    click
	held    bool // the left button went down in the view and is still down
	end     uv.Position
	endOK   bool // end is the last character's cell at the last frame, for Last
}

// click is where and when the left button last clicked without a drag, and the how-manieth click in a row.
type click struct {
	at point
	t  time.Time
	n  int
}

// anchor is the row-th row of the block-th block. Kept by block, the view stays on its content
// when a resize changes the blocks' heights.
type anchor struct{ block, row int }

// point is a cell of the transcript, its row counted from the transcript's start: a selection stays
// on its content while the view scrolls.
type point struct{ row, col int }

func (p point) before(q point) bool { return p.row < q.row || p.row == q.row && p.col < q.col }

// selection is the cells between from and to in reading order, both included. It's on, and highlighted,
// once a drag leaves the cell it started on, or while a double or triple click shows what it grabbed:
// a click alone highlights nothing.
type selection struct {
	from, to point
	on       bool
}

// ordered is the selection's first and last cell.
func (s selection) ordered() (point, point) {
	if s.to.before(s.from) {
		return s.to, s.from
	}
	return s.from, s.to
}

func (s selection) has(p point) bool {
	a, b := s.ordered()
	return s.on && !p.before(a) && !b.before(p)
}

func init() { module.Register("tui.fullscreen", &mod{}) }

func (m *mod) Init(b bus.Bus, _ config.Config) error {
	m.b, m.follow, m.calls = b, true, map[string]renderer.Block{}
	bus.Async(b, event.ByName(tui.Ready), func(_ context.Context, ev event.Event[tui.Screen]) {
		m.edit(func() { m.scr, m.handle = ev.Data(), ev.Data().Open(tui.Fullscreen, z, m) })
	})
	bus.Sync(b, prio, event.ByName(llm.UserMessage), func(_ context.Context, t tx.Tx, ev event.Event[string]) error {
		m.add(t, renderer.Text, "> "+ev.Data())
		return nil
	})
	bus.Sync(b, prio, event.ByName(llm.Error), func(_ context.Context, t tx.Tx, ev event.Event[string]) error {
		m.add(t, renderer.Text, "error: "+ev.Data())
		return nil
	})
	bus.Sync(b, prio, event.ByName(llm.Answer), m.answer)
	bus.Sync(b, prio, event.ByName(tool.Call), func(_ context.Context, t tx.Tx, ev event.Event[tool.Use]) error {
		u := ev.Data()
		// Line breaks in JSON are only between tokens: the input stays the same on one line.
		if blk := m.add(t, renderer.Tool, u.Name+" "+strings.ReplaceAll(string(u.Input), "\n", " ")+"\n"); blk != nil {
			m.calls[u.ID] = blk
			t.OnAbort(func() { delete(m.calls, u.ID) })
		}
		return nil
	})
	bus.Sync(b, prio, event.ByName(tool.Result), func(_ context.Context, t tx.Tx, ev event.Event[tool.Output]) error {
		o := ev.Data()
		blk, ok := m.calls[o.ID]
		if !ok {
			return nil // a call not shown, one the provider answered itself
		}
		delete(m.calls, o.ID)
		t.OnAbort(func() { m.calls[o.ID] = blk })
		status := "ok\n"
		if o.Error {
			status = "error\n"
		}
		// shortcut: a rolled back result stays on screen, a Block can't unwrite; give it a reset if that happens
		m.edit(func() { blk.Write(status + o.Text) })
		return nil
	})
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	return m.b.Dispatch(ctx, event.New(caret.Announce, caret.Source(m)))
}

func (m *mod) Stop(context.Context) error { return nil }

func (m *mod) Last() (uv.Position, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.end, m.endOK
}

// add appends a Block of kind with text, for as long as t isn't rolled back; nil if nothing renders it.
func (m *mod) add(t tx.Tx, kind, text string) renderer.Block {
	b := renderer.New(kind)
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
	m.paint(scr, area, hs, m.top)
	m.findEnd(scr, area, hs)
	if !m.sel.on {
		return
	}
	first := offset(hs, m.top)
	for y := range m.rows {
		for x := range m.w {
			if c := scr.CellAt(area.Min.X+x, area.Min.Y+y); c != nil && !c.IsZero() && m.sel.has(point{first + y, x}) {
				hl := *c
				hl.Style.Attrs |= uv.AttrReverse
				scr.SetCell(area.Min.X+x, area.Min.Y+y, &hl)
			}
		}
	}
}

// findEnd finds the last character on scr, drawn from hs: the rightmost set cell of the last row that has one.
// An empty last block, an answer before its first chunk, ends before its first row instead.
func (m *mod) findEnd(scr uv.Screen, area uv.Rectangle, hs []int) {
	m.endOK = m.follow && len(hs) > 0
	if !m.endOK {
		return
	}
	y := offset(hs, anchor{block: len(hs)}) - offset(hs, m.top) - gap // the row after the last block's text
	if hs[len(hs)-1] == gap {
		m.end = uv.Pos(-1, y)
		return
	}
	for y--; y >= 0; y-- {
		for x := m.w - 1; x >= 0; x-- {
			if c := scr.CellAt(area.Min.X+x, area.Min.Y+y); c != nil && !c.IsZero() && c.Content != " " {
				m.end = uv.Pos(x+c.Width-1, y)
				return
			}
		}
	}
	m.endOK = false
}

// paint draws the blocks into area from a on.
func (m *mod) paint(scr uv.Screen, area uv.Rectangle, hs []int, a anchor) {
	y := area.Min.Y - a.row
	for i := a.block; i < len(m.blocks) && y < area.Max.Y; i++ {
		m.blocks[i].Draw(scr, uv.Rect(area.Min.X, y, area.Dx(), hs[i]-gap))
		y += hs[i]
	}
}

func (m *mod) Input(ev uv.Event) bool {
	switch ev := ev.(type) {
	case uv.MouseClickEvent, uv.MouseMotionEvent, uv.MouseReleaseEvent:
		return m.selectBy(ev.(uv.MouseEvent))
	}
	page := max(m.rows-1, 1) // only Draw sets rows, on this goroutine
	var n int
	switch k := ev.(type) {
	case uv.MouseWheelEvent:
		switch k.Button {
		case uv.MouseWheelUp:
			n = -wheel
		case uv.MouseWheelDown:
			n = wheel
		default:
			return false
		}
	case uv.KeyPressEvent:
		switch {
		case k.MatchString("up"):
			n = -1
		case k.MatchString("down"):
			n = 1
		case k.MatchString("pgup"):
			n = -page
		case k.MatchString("pgdown"):
			n = page
		default:
			return false
		}
	default:
		return false
	}
	m.edit(func() {
		hs := m.heights()
		m.move(hs, offset(hs, m.top)+n)
	})
	return true
}

// selectBy selects with the left button: a press in the view starts a selection, a drag moves its end,
// and the release copies it to the clipboard and drops it, as tmux does.
func (m *mod) selectBy(ev uv.MouseEvent) bool {
	mo := ev.Mouse()
	if mo.Button != uv.MouseLeft {
		return false // the middle button pastes: the compositor's
	}
	_, press := ev.(uv.MouseClickEvent)
	taken := false
	m.edit(func() {
		if press && (mo.X >= m.w || mo.Y >= m.rows) || !press && !m.held {
			return // a press on a pane below, or the drag from there
		}
		taken = true
		hs := m.heights()
		p := point{offset(hs, m.top) + min(max(mo.Y, 0), m.rows-1), min(max(mo.X, 0), m.w-1)}
		switch ev.(type) {
		case uv.MouseClickEvent:
			m.sel, m.held = selection{from: p, to: p}, true
		case uv.MouseMotionEvent:
			m.sel.to = p
			m.sel.on = m.sel.on || p != m.sel.from
		case uv.MouseReleaseEvent:
			drag := m.sel.on || p != m.sel.from
			m.sel.to, m.sel.on, m.held = p, false, false
			if m.scr == nil {
				return
			}
			if drag {
				if text := m.selected(hs); text != "" {
					m.scr.Copy(text)
				}
				return
			}
			now := time.Now()
			if p != m.last.at || now.Sub(m.last.t) > doubleClick {
				m.last.n = 0
			}
			m.last = click{p, now, m.last.n%3 + 1}
			switch m.last.n {
			case 2:
				m.word(hs, p)
			case 3: // shortcut: the row, not the whole line a block wrapped; ask blocks for lines if that matters
				m.grab(hs, point{p.row, 0}, point{p.row, m.w - 1})
			}
		}
	})
	return taken
}

// word grabs the word at p.
func (m *mod) word(hs []int, p point) {
	scr := uv.NewWindow(m.w, 1, nil)
	m.paint(scr, scr.Bounds(), hs, at(hs, p.row))
	l := scr.Line(0)
	in := func(x int) bool { return l[x].IsZero() || !strings.ContainsAny(l[x].Content, separators) } // zero: a wide cell's tail
	if !in(p.col) {
		return
	}
	from, to := p.col, p.col
	for from > 0 && in(from-1) {
		from--
	}
	for to+1 < len(l) && in(to+1) {
		to++
	}
	m.grab(hs, point{p.row, from}, point{p.row, to})
}

// grab selects from..to and copies it, as terminals do on a double or triple click; the highlight goes after flash.
func (m *mod) grab(hs []int, from, to point) {
	m.sel = selection{from, to, true}
	text := m.selected(hs)
	if text == "" {
		m.sel.on = false
		return
	}
	m.scr.Copy(text)
	sel := m.sel
	time.AfterFunc(flash, func() {
		m.edit(func() {
			if m.sel == sel { // not a new one since
				m.sel.on = false
			}
		})
	})
}

// selected is the selection's text: its rows drawn again off screen, trailing blanks trimmed.
func (m *mod) selected(hs []int) string {
	a, b := m.sel.ordered()
	scr := uv.NewWindow(m.w, b.row-a.row+1, nil)
	m.paint(scr, scr.Bounds(), hs, at(hs, a.row))
	lines := make([]string, scr.Height())
	for y := range lines {
		l := scr.Line(y)
		if y == len(lines)-1 {
			l = l[:b.col+1]
		}
		if y == 0 {
			l = l[a.col:]
		}
		lines[y] = l.String()
	}
	// shortcut: a row a block wrapped gets a newline too; ask blocks for their text if that matters for copying.
	return strings.Join(lines, "\n")
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
	m.top = at(hs, pos)
}

// at is the anchor of the transcript's row pos.
func at(hs []int, pos int) anchor {
	var a anchor
	for a.block < len(hs) && pos >= hs[a.block] {
		pos -= hs[a.block]
		a.block++
	}
	a.row = pos
	return a
}

// offset is a's row counted from the transcript's start.
func offset(hs []int, a anchor) int {
	pos := a.row
	for _, h := range hs[:min(a.block, len(hs))] {
		pos += h
	}
	return pos
}
