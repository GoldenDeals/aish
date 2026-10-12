// Package codeblock registers fenced code with a language with the renderer core, ```lang … ```,
// highlighted by chroma.
package codeblock

import (
	"image/color"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

var (
	theme = styles.Get("monokai") // shortcut: one dark theme; add a config for light terminals if asked
	plain = theme.Get(chroma.Text).Colour
)

func init() {
	renderer.Register(renderer.Rule{Open: "^( *)```([\\w+#.-]+)[^\\n]*\\n", Close: "^ *```", Draw: draw})
}

func draw(z renderer.Zone, w int) string {
	n, text := len(z.Open[1])+2, strings.TrimSuffix(z.Text, "\n")
	lexer := lexers.Get(z.Open[2])
	if lexer == nil {
		lexer = lexers.Fallback
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return renderer.Indent(text, n, w)
	}
	var sb strings.Builder
	for _, t := range it.Tokens() {
		e, st := theme.Get(t.Type), uv.Style{}
		if e.Colour.IsSet() && e.Colour != plain { // plain text keeps the terminal's colour, not the theme's
			st.Fg = color.RGBA{e.Colour.Red(), e.Colour.Green(), e.Colour.Blue(), 0xff}
		}
		if e.Bold == chroma.Yes {
			st.Attrs |= uv.AttrBold
		}
		if e.Italic == chroma.Yes {
			st.Attrs |= uv.AttrItalic
		}
		for i, part := range strings.Split(t.Value, "\n") { // styled line by line: Indent puts spaces after each break
			if i > 0 {
				sb.WriteByte('\n')
			}
			if part != "" {
				sb.WriteString(renderer.Styled(st, part))
			}
		}
	}
	return renderer.Indent(sb.String(), n, w)
}
