// Package renderer is the contract between the modules that show content and the renderer core
// (modules/frontend/tui/renderer), which draws it by the rules of the renderers built in.
//
// The source of the content decides its kind: an llm.answer is Markdown, a user message is Text, a tool call is Tool.
package renderer

import (
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/contracts/tui"
)

// Kinds of content.
const (
	Text     = "text"     // shown as is
	Markdown = "markdown" // parsed by the renderers' rules: an llm.answer
	// Tool is a tool call: a line with the tool's name, a space and its input as JSON on one line;
	// once the call is done, a line "ok" or "error" and the output.
	Tool = "tool"
)

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

var core func(kind string) Block

// Provide installs the renderer core; it calls it from init.
func Provide(f func(kind string) Block) { core = f }

// New returns an empty Block for content of kind; nil when the renderer core isn't built.
func New(kind string) Block {
	if core == nil {
		return nil
	}
	return core(kind)
}
