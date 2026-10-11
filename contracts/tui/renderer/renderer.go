// Package renderer is the bus contract between the modules that show content and the renderers that draw it.
//
// A renderer announces itself once, from Start, with an Announce event. A module that shows content collects
// the renderers (subscribe in Init) and asks for a Block of the content's kind; when no renderer has that kind,
// it falls back to Text. The source of the content decides its kind: an llm.answer is Markdown, a tool says what
// its output is. A renderer of a kind that holds others, like a markdown fenced block, gets Blocks for them from pick.
//
// Renderers draw untrusted text: they must not let it reach the terminal as escape sequences.
package renderer

import (
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/contracts/tui"
)

// Announce is event.Event[Renderer]. Two renderers of one kind are an error.
const Announce = "tui.renderer.announce"

// Kinds of content.
const (
	Text     = "text"     // plain text, the fallback for every other kind
	Markdown = "markdown" // an llm.answer
)

type Renderer struct {
	Kind string
	New  func(pick Pick) Block // an empty Block
}

// Pick returns a Block of kind, falling back to Text; nil when there is no Text renderer either.
type Pick func(kind string) Block

// Block shows one piece of content. It is a tui element as is: opened as tui.Text, it prints into the scrollback.
// Its owner calls its methods one at a time.
type Block interface {
	// Draw paints the content in area, which may stick out of scr: the part outside is cut.
	uv.Drawable
	// Size reports the rows the content takes at a width; pass math.MaxInt as the height.
	tui.Sizer
	// Write appends content as it streams.
	Write(chunk string)
}
