package codeblock

import (
	"math"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	contract "github.com/GoldenDeals/aish-core/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

func TestHighlight(t *testing.T) {
	b := renderer.New(contract.Markdown)
	b.Write("```go\nfunc main() {}\n```")
	_, h := b.Size(20, math.MaxInt)
	scr := uv.NewWindow(20, h, nil)
	b.Draw(scr, scr.Bounds())
	if got, want := scr.String(), "  func main() {}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if c := scr.CellAt(2, 0); c.Content != "f" || c.Style.Fg == nil { // the keyword
		t.Errorf("func: %+v", c)
	}
	if c := scr.CellAt(11, 0); c.Content != "(" || c.Style.Fg != nil { // plain text: the terminal's colour
		t.Errorf("(: %+v", c)
	}
}
