// Package math registers LaTeX math with the renderer core, typeset in Unicode by termtex: $$…$$, \[…\] and
// ```math … ``` on rows of their own, $…$ and \(…\) within a line. A formula that doesn't parse shows its source.
package math

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/doug/termtex"

	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

func init() {
	renderer.Register(
		renderer.Rule{Open: `\$\$`, Close: `\$\$`, Draw: display},
		renderer.Rule{Open: `\\\[`, Close: `\\\]`, Draw: display},
		renderer.Rule{Open: "^ *```math *\\n", Close: "^ *```", Draw: display},
		renderer.Rule{Open: `\$`, Close: `\$`, Abort: `\n`, Draw: inline},
		renderer.Rule{Open: `\\\(`, Close: `\\\)`, Abort: `\n`, Draw: inline},
	)
}

// source is the zone as written.
func source(z renderer.Zone) string {
	if z.Close == nil {
		return z.Open[0] + z.Text
	}
	return z.Open[0] + z.Text + z.Close[0]
}

func display(z renderer.Zone, w int) string {
	if z.Close != nil { // a formula half written doesn't parse: shown as source until it closes
		if out, err := termtex.Render(strings.TrimSpace(z.Text), termtex.Style{Width: w - 2}); err == nil {
			return renderer.Indent(strings.TrimRight(out, "\n"), 2, w)
		}
	}
	return renderer.Styled(uv.Style{Attrs: uv.AttrFaint}, source(z))
}

func inline(z renderer.Zone, _ int) string {
	if strings.TrimSpace(z.Text) != z.Text { // $5 and $10: money, not math
		return source(z)
	}
	out, err := termtex.Render(z.Text, termtex.Style{Inline: true})
	if err != nil || strings.Contains(out, "\n") {
		return source(z)
	}
	return out
}
