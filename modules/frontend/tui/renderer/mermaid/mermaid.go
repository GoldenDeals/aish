// Package mermaid registers ```mermaid … ``` with the renderer core: diagrams drawn in box characters once the fence
// closes, their source until then and for kinds it can't draw. mermaid-ascii draws flowcharts, sequence and ER
// diagrams, and state and class diagrams turned into its flowcharts; mindmaps, pie and gantt charts and git graphs
// are drawn here.
package mermaid

import (
	"io"
	"regexp"
	"strings"

	"github.com/AlexanderGrooff/mermaid-ascii/pkg/diagram"
	"github.com/AlexanderGrooff/mermaid-ascii/pkg/render"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/sirupsen/logrus"

	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

// mermaid-ascii reads a node as id[label] and an edge label as -->|label|: the rest of mermaid's syntax that
// models write it would take for node names, so it's rewritten to those or dropped.
var (
	styling = regexp.MustCompile(`(?m)^\s*(?:style|classDef|class|linkStyle|click)\b.*\n?`)
	labeled = regexp.MustCompile(`(--|-\.|==)\s*"?([^-.=>|\s"][^\n"]*?)"?\s*(-->|\.->|==>)`) // A -- yes --> B
	shaped  = regexp.MustCompile(`(\w+)\s*(?:\(\(|\(\[|\[\(|\[\[|\{\{|\[/|\[\\|>|\()(.*?)(?:\)\)|\]\)|\)\]|\]\]|\}\}|/\]|\\\]|\)|\])`)
)

func init() {
	logrus.SetOutput(io.Discard) // mermaid-ascii warns on stderr, which would break the frame
	renderer.Register(renderer.Rule{Open: "^( *)```mermaid *\\n", Close: "^ *```", Draw: draw})
}

func draw(z renderer.Zone, w int) string {
	n := len(z.Open[1]) + 2
	if z.Close != nil {
		if out := strings.TrimRight(drawn(z.Text, w-n), "\n"); strings.TrimSpace(out) != "" {
			lines := strings.Split(out, "\n")
			for i, l := range lines {
				lines[i] = ansi.Truncate(strings.TrimRight(l, " "), max(w-n, 1), "") // a diagram that can't fit is cut, not wrapped
			}
			return renderer.Indent(strings.Join(lines, "\n"), n, w)
		}
	}
	return renderer.Indent(renderer.Styled(uv.Style{Attrs: uv.AttrFaint}, strings.TrimSuffix(z.Text, "\n")), n, w)
}

// drawn is the diagram src in w cells, or "" if it can't be drawn.
func drawn(src string, w int) string {
	kind := ""
	if f := strings.Fields(src); len(f) > 0 {
		kind = f[0]
	}
	switch kind {
	case "stateDiagram", "stateDiagram-v2":
		src = state(src)
	case "classDiagram", "classDiagram-v2":
		src = class(src)
	case "mindmap":
		return mindmap(src)
	case "pie":
		return pie(src, w)
	case "gantt":
		return gantt(src, w)
	case "gitGraph", "gitGraph:":
		return gitGraph(src)
	default:
		src = normal(src)
	}
	cfg := diagram.DefaultConfig()
	cfg.MaxWidth = w
	out, err := render.RenderDiagram(src, cfg)
	if err != nil {
		return ""
	}
	return out
}

// normal is a diagram in the syntax mermaid-ascii reads: no styling, edge labels in pipes, every shape a box.
// Emoji lose the variation selector that makes them two cells wide (⏸️): mermaid-ascii counts them as one.
func normal(s string) string {
	s = strings.ReplaceAll(s, "\uFE0F", "")
	s = styling.ReplaceAllString(s, "")
	s = labeled.ReplaceAllString(s, "$3|$2|")
	return shaped.ReplaceAllString(s, "$1[$2]")
}
