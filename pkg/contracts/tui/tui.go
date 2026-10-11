// Package tui is the bus contract between the terminal compositor and the modules that draw.
//
// The compositor owns the terminal and dispatches Ready once from its Start with a Screen. A module keeps
// the Screen and opens elements on it: it picks the kind and z, the compositor decides where the element goes.
// An element is any uv.Drawable. It draws in its own coordinates: area is scr.Bounds() with the origin at 0,0,
// nothing outside it is visible, and cells it doesn't set are transparent. To paint a background, fill it.
//
// Elements are ordered by z, then by opening order: a higher one is drawn later, gets input first and,
// among panes, sits closer to the bottom edge. Input goes down that order until a Receiver consumes it;
// an unconsumed ctrl+c interrupts the process. An element of zero size is invisible but still gets input:
// that's how a module adds a global hotkey.
//
// Draw, Size, Input and Cursor are called one at a time from the compositor's goroutine. Guard what your
// bus handlers change with a mutex, and never block there: Dispatch waits while another tx is open
// (an llm.answer stream holds one until it ends), so call it in a goroutine.
// While the compositor runs, don't write to stdout or stderr: print through a Text element.
package tui

import uv "github.com/charmbracelet/ultraviolet"

// Ready is event.Event[Screen]. It may come before your Start: subscribe in Init.
// Dispatch it as event.New[Screen], typed subscribers skip any other payload type.
const Ready = "tui.ready"

type Kind int

const (
	Pane       Kind = iota // full width, stacked at the bottom; height from Size
	Fullscreen             // the rows panes leave; the terminal is in the alt screen while one is open
	Overlay                // w×h from Size, centered over everything below it
	Text                   // drawn once at h rows from Size(width, math.MaxInt), printed into the scrollback, then closed
)

type Screen interface {
	// Open adds el. It doesn't block and is safe from any goroutine, Draw and Input included.
	Open(k Kind, z int, el uv.Drawable) Handle
}

type Handle interface {
	Redraw() // ask for a new frame after a change; calls made while a frame is drawn coalesce into the next one
	Close()  // remove the element; calling either method afterwards is harmless
}

// Sizer reports the size an element wants out of width×height; it may get less.
// An element without it takes all it is offered, and a Text one is a screen high.
type Sizer interface {
	Size(width, height int) (w, h int)
}

// Receiver gets key, paste and other terminal events; true consumes ev. Input alone draws no frame:
// call Redraw if it changed the element.
type Receiver interface {
	Input(ev uv.Event) bool
}

// Cursorer places the terminal cursor, in the element's coordinates. The topmost element
// returning a non-nil cursor gets it; when there's none, the cursor is hidden.
type Cursorer interface {
	Cursor() *uv.Cursor
}
