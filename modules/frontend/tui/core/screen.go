package core

import (
	"math"
	"slices"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/pkg/contracts/tui"
)

// item is an open element; rect is its place in the frame, set by layout.
type item struct {
	kind tui.Kind
	z    int
	el   uv.Drawable
	rect uv.Rectangle
}

// size asks el for its size out of w×h and keeps it within.
func size(el uv.Drawable, w, h int) (int, int) {
	if s, ok := el.(tui.Sizer); ok {
		sw, sh := s.Size(w, h)
		return min(max(sw, 0), w), min(max(sh, 0), h)
	}
	return w, h
}

// layout places items, ordered bottom to top, on a w×h terminal. It returns the frame height:
// the whole terminal in the alt screen, the rows panes and overlays need inline.
func layout(items []*item, w, h int) (frameH int, alt bool) {
	rows := h // free rows above the panes placed so far, counted against the terminal's bottom
	for _, it := range slices.Backward(items) {
		switch it.kind {
		case tui.Pane:
			_, ph := size(it.el, w, rows)
			rows -= ph
			it.rect = uv.Rect(0, rows, w, ph)
		case tui.Overlay:
			ow, oh := size(it.el, w, h)
			it.rect = uv.Rect(0, 0, ow, oh)
			frameH = max(frameH, oh)
		case tui.Fullscreen:
			alt = true
		}
	}
	frameH = max(frameH, h-rows)
	if alt {
		frameH = h
	}
	for _, it := range items {
		switch it.kind {
		case tui.Pane:
			it.rect = it.rect.Sub(uv.Pos(0, h-frameH))
		case tui.Overlay:
			it.rect = it.rect.Add(uv.Pos((w-it.rect.Dx())/2, (frameH-it.rect.Dy())/2))
		case tui.Fullscreen:
			it.rect = uv.Rect(0, 0, w, rows)
		}
	}
	return frameH, alt
}

// compose draws items bottom to top onto scr and returns the cursor of the topmost one that has it, in frame coordinates.
func compose(scr uv.Screen, items []*item) (cur *uv.Cursor) {
	for _, it := range items {
		if it.rect.Empty() {
			continue
		}
		s := newSurface(it.rect.Dx(), it.rect.Dy(), scr.WidthMethod())
		it.el.Draw(s, s.Bounds())
		s.copyTo(scr, it.rect.Min)
		if c, ok := it.el.(tui.Cursorer); ok {
			if c := c.Cursor(); c != nil && c.Position.In(s.Bounds()) {
				moved := *c
				moved.Position = c.Position.Add(it.rect.Min)
				cur = &moved
			}
		}
	}
	return cur
}

// input offers ev to receivers from the top down and reports whether one consumed it.
func input(items []*item, ev uv.Event) bool {
	for _, it := range slices.Backward(items) {
		if r, ok := it.el.(tui.Receiver); ok && r.Input(ev) {
			return true
		}
	}
	return false
}

// text draws a Text element at width w for printing; h, the screen height, is its height without a Sizer.
func text(el uv.Drawable, w, h int, wm uv.WidthMethod) string {
	if s, ok := el.(tui.Sizer); ok {
		_, h = s.Size(w, math.MaxInt)
	}
	s := newSurface(w, max(h, 0), wm)
	el.Draw(s, s.Bounds())
	return s.Render()
}

// surface is an element's own screen: it records the cells set, the rest stay transparent.
// A zero cell can't mark them: in uv it is the placeholder after a wide cell.
type surface struct {
	*uv.Buffer
	wm    uv.WidthMethod
	drawn []bool
}

func newSurface(w, h int, wm uv.WidthMethod) *surface {
	return &surface{Buffer: uv.NewBuffer(w, h), wm: wm, drawn: make([]bool, w*h)}
}

func (s *surface) SetCell(x, y int, c *uv.Cell) {
	if uv.Pos(x, y).In(s.Bounds()) {
		s.drawn[y*s.Width()+x] = true
	}
	s.Buffer.SetCell(x, y, c)
}

func (s *surface) WidthMethod() uv.WidthMethod { return s.wm }

// copyTo sets the drawn cells on scr at off. Placeholders are skipped: setting one blanks its wide cell,
// and scr places them itself.
func (s *surface) copyTo(scr uv.Screen, off uv.Position) {
	w := s.Width()
	for i, drawn := range s.drawn {
		x, y := i%w, i/w
		if c := s.CellAt(x, y); drawn && c.Width > 0 {
			scr.SetCell(off.X+x, off.Y+y, c)
		}
	}
}
