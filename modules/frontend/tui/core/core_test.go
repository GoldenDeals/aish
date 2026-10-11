package core

import (
	"slices"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/pkg/contracts/tui"
)

// fake asks for w×h, paints its area with fill and then cells (positions may lie outside),
// logs input under name and consumes the key eat.
type fake struct {
	name   string
	w, h   int
	fill   string
	cells  map[uv.Position]string
	eat    string
	log    *[]string
	cursor *uv.Cursor
}

func (f *fake) Size(int, int) (int, int) { return f.w, f.h }

func (f *fake) Draw(scr uv.Screen, area uv.Rectangle) {
	for y := area.Min.Y; f.fill != "" && y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			scr.SetCell(x, y, uv.NewCell(scr.WidthMethod(), f.fill))
		}
	}
	for p, s := range f.cells {
		scr.SetCell(p.X, p.Y, uv.NewCell(scr.WidthMethod(), s))
	}
}

func (f *fake) Input(ev uv.Event) bool {
	if f.log != nil {
		*f.log = append(*f.log, f.name)
	}
	k, ok := ev.(uv.KeyPressEvent)
	return ok && f.eat != "" && k.MatchString(f.eat)
}

func (f *fake) Cursor() *uv.Cursor { return f.cursor }

// none has no Sizer, so it takes all it is offered.
var none = uv.DrawableFunc(func(uv.Screen, uv.Rectangle) {})

func key(code rune, mod uv.KeyMod) uv.KeyPressEvent {
	k := uv.KeyPressEvent{Code: code, Mod: mod}
	if mod == 0 {
		k.Text = string(code)
	}
	return k
}

func TestLayout(t *testing.T) {
	box := func(w, h int) *fake { return &fake{w: w, h: h} }
	tests := []struct {
		name   string
		items  []*item // bottom to top
		frameH int
		alt    bool
		rects  []uv.Rectangle
	}{
		{
			"inline panes stack, higher closer to the bottom",
			[]*item{{kind: tui.Pane, el: box(0, 2)}, {kind: tui.Pane, el: box(0, 1)}, {kind: tui.Pane, el: box(0, 1)}},
			4, false,
			[]uv.Rectangle{uv.Rect(0, 0, 20, 2), uv.Rect(0, 2, 20, 1), uv.Rect(0, 3, 20, 1)},
		},
		{
			"panes past the terminal lose rows from the bottom one",
			[]*item{{kind: tui.Pane, el: box(0, 3)}, {kind: tui.Pane, el: box(0, 8)}, {kind: tui.Pane, el: box(0, 4)}},
			10, false,
			[]uv.Rectangle{uv.Rect(0, 0, 20, 0), uv.Rect(0, 0, 20, 6), uv.Rect(0, 6, 20, 4)},
		},
		{
			"a pane without Sizer fills the rest",
			[]*item{{kind: tui.Pane, el: none}, {kind: tui.Pane, el: box(0, 1)}},
			10, false,
			[]uv.Rectangle{uv.Rect(0, 0, 20, 9), uv.Rect(0, 9, 20, 1)},
		},
		{
			"an overlay grows the inline frame",
			[]*item{{kind: tui.Pane, el: box(0, 1)}, {kind: tui.Overlay, el: box(6, 5)}},
			5, false,
			[]uv.Rectangle{uv.Rect(0, 4, 20, 1), uv.Rect(7, 0, 6, 5)},
		},
		{
			"an overlay is cut to the terminal",
			[]*item{{kind: tui.Overlay, el: box(30, 20)}},
			10, false,
			[]uv.Rectangle{uv.Rect(0, 0, 20, 10)},
		},
		{
			"fullscreen takes the alt screen above the panes",
			[]*item{{kind: tui.Fullscreen, el: none}, {kind: tui.Fullscreen, el: box(1, 1)}, {kind: tui.Pane, el: box(0, 1)}, {kind: tui.Overlay, el: box(4, 2)}},
			10, true,
			[]uv.Rectangle{uv.Rect(0, 0, 20, 9), uv.Rect(0, 0, 20, 9), uv.Rect(0, 9, 20, 1), uv.Rect(8, 4, 4, 2)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frameH, alt := layout(tt.items, 20, 10)
			if frameH != tt.frameH || alt != tt.alt {
				t.Errorf("frame %d alt %v, want %d %v", frameH, alt, tt.frameH, tt.alt)
			}
			for i, it := range tt.items {
				if it.rect != tt.rects[i] {
					t.Errorf("item %d at %v, want %v", i, it.rect, tt.rects[i])
				}
			}
		})
	}
}

func TestComposeTransparentAndClipped(t *testing.T) {
	top := &fake{cells: map[uv.Position]string{
		{X: 0, Y: 0}: "#", {X: 2, Y: 1}: "#",
		{X: -1, Y: 0}: "x", {X: 3, Y: 0}: "x", {X: 0, Y: 2}: "x", // outside its 3×2 area
	}}
	scr := uv.NewWindow(5, 3, nil)
	compose(scr, []*item{
		{el: &fake{fill: "."}, rect: uv.Rect(0, 0, 5, 3)},
		{el: top, rect: uv.Rect(1, 0, 3, 2)},
	})
	if got, want := scr.String(), ".#...\n...#.\n....."; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestComposeWide(t *testing.T) {
	wide := func(rect uv.Rectangle) *item {
		return &item{el: &fake{cells: map[uv.Position]string{{}: "世"}}, rect: rect}
	}
	tests := []struct {
		name  string
		items []*item
		want  string
	}{
		{"its placeholder over a drawn cell isn't copied over it",
			[]*item{{el: &fake{fill: ".", cells: map[uv.Position]string{{}: "世"}}, rect: uv.Rect(0, 0, 4, 1)}}, "世.."},
		{"a transparent element over its second half keeps it",
			[]*item{wide(uv.Rect(0, 0, 4, 1)), {el: &fake{}, rect: uv.Rect(1, 0, 1, 1)}}, "世"},
		{"a cell drawn over its second half replaces it",
			[]*item{wide(uv.Rect(0, 0, 4, 1)), {el: &fake{fill: "x"}, rect: uv.Rect(1, 0, 1, 1)}}, " x"},
		{"an element too narrow for it gets a blank",
			[]*item{{el: &fake{fill: "."}, rect: uv.Rect(0, 0, 4, 1)}, wide(uv.Rect(1, 0, 1, 1))}, ". .."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scr := uv.NewWindow(4, 1, nil)
			compose(scr, tt.items)
			if got := scr.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComposeCursor(t *testing.T) {
	cur := compose(uv.NewWindow(4, 3, nil), []*item{
		{el: &fake{cursor: uv.NewCursor(1, 0)}, rect: uv.Rect(0, 2, 4, 1)},
		{el: &fake{}, rect: uv.Rect(0, 0, 4, 1)},                           // no cursor: the one below shows
		{el: &fake{cursor: uv.NewCursor(5, 0)}, rect: uv.Rect(0, 1, 4, 1)}, // outside its area: ignored
	})
	if cur == nil || cur.Position != uv.Pos(1, 2) {
		t.Errorf("cursor %v, want at 1,2", cur)
	}
}

func TestInput(t *testing.T) {
	var log []string
	items := []*item{
		{el: &fake{name: "low", log: &log}},
		{el: &fake{name: "mid", log: &log, eat: "a"}},
		{el: none}, // no Receiver
		{el: &fake{name: "top", log: &log}},
	}
	if !input(items, key('a', 0)) || !slices.Equal(log, []string{"top", "mid"}) {
		t.Errorf("consumed a: %v", log)
	}
	log = nil
	if input(items, key('b', 0)) || !slices.Equal(log, []string{"top", "mid", "low"}) {
		t.Errorf("nobody consumed b: %v", log)
	}
}

func TestOpenClose(t *testing.T) {
	m := &mod{wake: make(chan struct{}, 1)}
	a := m.Open(tui.Pane, 0, &fake{name: "a"})
	m.Open(tui.Pane, 0, &fake{name: "b"}) // equal z: opened later is above
	m.Open(tui.Overlay, -1, &fake{name: "c"})
	m.Open(tui.Pane, 5, &fake{name: "d"})
	text := m.Open(tui.Text, 9, &fake{name: "t"})
	a.Close()
	text.Close() // closed before its frame: never printed
	a.Close()    // again: harmless

	var names []string
	for _, it := range m.items {
		names = append(names, it.el.(*fake).name)
	}
	if want := []string{"c", "b", "d"}; !slices.Equal(names, want) || len(m.texts) != 0 {
		t.Errorf("items %v texts %d, want %v and none", names, len(m.texts), want)
	}
	if len(m.wake) != 1 {
		t.Error("no frame requested")
	}
}

func TestCtrlC(t *testing.T) {
	defer func(f func()) { interrupt = f }(interrupt)
	n := 0
	interrupt = func() { n++ }

	m := &mod{wake: make(chan struct{}, 1)}
	m.event(key('c', uv.ModCtrl)) // nobody consumes it
	h := m.Open(tui.Overlay, 0, &fake{eat: "ctrl+c"})
	m.event(key('c', uv.ModCtrl)) // consumed
	h.Close()
	m.event(key('a', 0))
	if n != 1 {
		t.Errorf("interrupted %d times, want 1", n)
	}
}

func TestEventFrames(t *testing.T) {
	m := &mod{wake: make(chan struct{}, 1)}
	m.event(key('a', 0))
	if len(m.wake) != 0 {
		t.Error("input alone asked for a frame")
	}
	m.event(uv.WindowSizeEvent{Width: 80, Height: 24})
	if len(m.wake) != 1 || m.w != 80 || m.h != 24 {
		t.Errorf("resize: frame due %v, size %dx%d", len(m.wake) == 1, m.w, m.h)
	}
}

func TestText(t *testing.T) {
	wm := uv.NewWindow(0, 0, nil).WidthMethod()
	el := &fake{w: 99, h: 2, cells: map[uv.Position]string{{X: 0, Y: 0}: "h", {X: 1, Y: 0}: "i", {X: 0, Y: 1}: "!"}}
	if got, want := text(el, 5, 10, wm), "hi   \n!    "; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := strings.Count(text(none, 5, 3, wm), "\n"); got != 2 {
		t.Errorf("without Sizer: %d lines, want a screen of 3", got+1)
	}
}
