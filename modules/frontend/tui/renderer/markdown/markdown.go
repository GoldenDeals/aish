// Package markdown registers markdown with the renderer core. The rules cover headings, lists, tasks, quotes,
// rules, tables, footnotes, definitions, emphasis, code spans, links, escapes, emoji, and inline HTML.
// A fence without a language is markdown's too; a fence with one is left to a code renderer.
package markdown

import (
	"regexp"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark-emoji/definition"

	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

var (
	bold   = uv.Style{Attrs: uv.AttrBold}
	italic = uv.Style{Attrs: uv.AttrItalic}
	dim    = uv.Style{Attrs: uv.AttrFaint}
	strike = uv.Style{Attrs: uv.AttrStrikethrough}
	mark   = uv.Style{Attrs: uv.AttrReverse}
	under  = uv.Style{Underline: uv.UnderlineSingle}
	code   = uv.Style{Fg: ansi.Cyan}

	tags   = map[string]uv.Style{"kbd": mark, "mark": mark, "b": bold, "strong": bold, "i": italic, "em": italic, "s": strike, "del": strike, "u": under, "ins": under, "code": code}
	emojis = definition.Github()
	sup    = scripts("0123456789+-=()abcdefghijklmnoprstuvwxyz", "⁰¹²³⁴⁵⁶⁷⁸⁹⁺⁻⁼⁽⁾ᵃᵇᶜᵈᵉᶠᵍʰⁱʲᵏˡᵐⁿᵒᵖʳˢᵗᵘᵛʷˣʸᶻ")
	sub    = scripts("0123456789+-=()aehijklmnoprstuvx", "₀₁₂₃₄₅₆₇₈₉₊₋₌₍₎ₐₑₕᵢⱼₖₗₘₙₒₚᵣₛₜᵤᵥₓ")
	delim  = regexp.MustCompile(`^ *(:?)-+(:?) *$`)
	attr   = regexp.MustCompile(`(alt|src)\s*=\s*"([^"]*)"`)
)

func init() {
	renderer.Register(
		// blocks: a line each, from the marker to the line break it leaves; the order breaks ties
		renderer.Rule{Open: `^ *(?:-{3,}|\*{3,}|_{3,}) *$`, Draw: hr},
		renderer.Rule{Open: `^(#{1,6}) +`, Close: `\n`, Nested: true, Draw: heading},
		renderer.Rule{Open: `^( *)[-*+] `, Close: `\n`, Nested: true, Draw: bullet},
		renderer.Rule{Open: `^( *)(\d+)[.)] `, Close: `\n`, Nested: true, Draw: numbered},
		renderer.Rule{Open: `^( *)[-*+] \[([ xX])\] `, Close: `\n`, Nested: true, Draw: task},
		renderer.Rule{Open: `^((?:> ?)+)`, Close: `\n`, Nested: true, Draw: quote},
		renderer.Rule{Open: `^\[\^([^\]]+)\]: `, Close: `\n`, Nested: true, Draw: footnote},
		renderer.Rule{Open: `^: `, Close: `\n`, Nested: true, Draw: describe},
		renderer.Rule{Open: "^( *)``` *\n", Close: "^ *```", Draw: fence},
		renderer.Rule{Open: `^ *\|`, Close: `\n\n`, Nested: true, Draw: table},
		// inline
		renderer.Rule{Open: ` (?:\*+|_+|~+|=+|\++|\^) `, Draw: func(z renderer.Zone, _ int) string { return z.Open[0] }}, // shortcut: no flanking rules but this: 2 * 3 is text
		renderer.Rule{Open: `\*\*`, Close: `\*\*`, Abort: `\n`, Nested: true, Draw: styled(bold)},
		renderer.Rule{Open: `\b__`, Close: `__\b`, Abort: `\n`, Nested: true, Draw: styled(bold)},
		renderer.Rule{Open: `\*`, Close: `\*`, Abort: `\n`, Nested: true, Draw: styled(italic)},
		renderer.Rule{Open: `\b_`, Close: `_\b`, Abort: `\n`, Nested: true, Draw: styled(italic)},
		renderer.Rule{Open: `~~`, Close: `~~`, Abort: `\n`, Nested: true, Draw: styled(strike)},
		renderer.Rule{Open: `==`, Close: `==`, Abort: `\n`, Nested: true, Draw: styled(mark)},
		renderer.Rule{Open: `\+\+`, Close: `\+\+`, Abort: `\n`, Nested: true, Draw: styled(under)},
		renderer.Rule{Open: `\^`, Close: `\^`, Abort: `\s`, Draw: script(sup)},
		renderer.Rule{Open: `~`, Close: `~`, Abort: `\s`, Draw: script(sub)},
		renderer.Rule{Open: "``", Close: "``", Abort: `\n`, Draw: span},
		renderer.Rule{Open: "`", Close: "`", Abort: `\n`, Draw: span},
		renderer.Rule{Open: `!?\[`, Close: `\]\(([^)\s]*)\)`, Abort: `\n`, Nested: true, Draw: link},
		renderer.Rule{Open: `<(https?://[^>\s]+)>`, Draw: url},
		renderer.Rule{Open: `https?://[^\s<>()\[\]]+`, Draw: url},
		renderer.Rule{Open: `\[\^([^\]\s]+)\]`, Draw: func(z renderer.Zone, _ int) string { return renderer.Styled(dim, "["+z.Open[1]+"]") }},
		renderer.Rule{Open: `\\([!-/:-@\[-` + "`" + `{-~])`, Draw: func(z renderer.Zone, _ int) string { return z.Open[1] }},
		renderer.Rule{Open: `:([a-z0-9_+-]+):`, Draw: emoji},
		// HTML: the tags a terminal can show; the others are dropped, their text stays
		renderer.Rule{Open: `<(kbd|mark|b|strong|i|em|s|del|u|ins|code|sub|sup)>`, Close: `</[a-z]+>`, Abort: `\n\n`, Nested: true, Draw: tag},
		renderer.Rule{Open: `<br\s*/?>`, Draw: func(renderer.Zone, int) string { return "\n" }},
		renderer.Rule{Open: `<!--`, Close: `-->`, Draw: nothing},
		renderer.Rule{Open: `<img\s[^>]*>`, Draw: img},
		renderer.Rule{Open: `</?[a-zA-Z][^>\n]*>`, Draw: nothing},
	)
}

func styled(st uv.Style) func(renderer.Zone, int) string {
	return func(z renderer.Zone, w int) string { return renderer.Styled(st, z.Inner(w)) }
}

// span is a code span; a space on both sides is padding, so “ `x` “ can hold backticks.
func span(z renderer.Zone, _ int) string {
	t := z.Text
	if len(t) > 2 && t[0] == ' ' && t[len(t)-1] == ' ' && strings.TrimSpace(t) != "" {
		t = t[1 : len(t)-1]
	}
	return renderer.Styled(code, t)
}

func nothing(renderer.Zone, int) string { return "" }

func hr(_ renderer.Zone, w int) string { return renderer.Styled(dim, strings.Repeat("─", w)) }

func heading(z renderer.Zone, w int) string {
	st := bold
	if z.Open[1] == "#" {
		st.Underline = uv.UnderlineSingle
	}
	return renderer.Styled(st, z.Inner(w))
}

// item is z's text after marker, indent columns in, wrapped with its next lines under the text.
func item(indent int, marker string, z renderer.Zone, w int) string {
	pad := indent + ansi.StringWidth(marker)
	body := ansi.Wrap(z.Inner(max(w-pad, 1)), max(w-pad, 1), "")
	return strings.Repeat(" ", indent) + marker + strings.ReplaceAll(body, "\n", "\n"+strings.Repeat(" ", pad))
}

func bullet(z renderer.Zone, w int) string   { return item(len(z.Open[1]), "• ", z, w) }
func numbered(z renderer.Zone, w int) string { return item(len(z.Open[1]), z.Open[2]+". ", z, w) }
func footnote(z renderer.Zone, w int) string {
	return item(0, renderer.Styled(dim, "["+z.Open[1]+"]")+" ", z, w)
}
func describe(z renderer.Zone, w int) string { return item(2, "", z, w) }

func task(z renderer.Zone, w int) string {
	box := "☐ "
	if z.Open[2] != " " {
		box = "☑ "
	}
	return item(len(z.Open[1]), box, z, w)
}

func quote(z renderer.Zone, w int) string {
	depth := strings.Count(z.Open[1], ">")
	bar := renderer.Styled(dim, strings.Repeat("│ ", depth))
	body := ansi.Wrap(z.Inner(max(w-2*depth, 1)), max(w-2*depth, 1), "")
	return bar + strings.ReplaceAll(body, "\n", "\n"+bar)
}

func fence(z renderer.Zone, w int) string {
	return renderer.Indent(strings.TrimSuffix(z.Text, "\n"), len(z.Open[1])+2, w)
}

// table lays out rows split on |, aligned by the :--: row under the header and fitted to w; without that row
// it's not a table and stays as written.
func table(z renderer.Zone, w int) string {
	inner := z.Inner(w)
	lines := strings.Split(strings.TrimRight(inner, "\n"), "\n")
	var rows [][]string
	var rest []string // lines after the table that aren't rows of it
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if i > 0 {
			if !strings.HasPrefix(l, "|") {
				rest = lines[i:]
				break
			}
			l = l[1:]
		}
		rows = append(rows, strings.Split(strings.TrimSuffix(l, "|"), "|"))
	}
	if len(rows) < 2 || len(rows[1]) != len(rows[0]) {
		return "|" + inner
	}
	n, align := len(rows[0]), make([]byte, len(rows[0]))
	for i, c := range rows[1] {
		m := delim.FindStringSubmatch(ansi.Strip(c))
		switch {
		case m == nil:
			return "|" + inner
		case m[1] != "" && m[2] != "":
			align[i] = 'c'
		case m[2] != "":
			align[i] = 'r'
		}
	}
	rows = append(rows[:1], rows[2:]...)
	widths := make([]int, n)
	for _, r := range rows {
		for i := range min(len(r), n) {
			r[i] = strings.TrimSpace(r[i])
			widths[i] = max(widths[i], ansi.StringWidth(r[i]))
		}
	}
	for total := sum(widths) + 3*(n-1); total > w; total-- { // shrink the widest column until the table fits
		i := widest(widths)
		if widths[i] <= 1 {
			break
		}
		widths[i]--
	}
	sep := renderer.Styled(dim, " │ ")
	var out []string
	for ri, r := range rows {
		cells, h := make([][]string, n), 1
		for i := range n {
			c := ""
			if i < len(r) {
				c = r[i]
			}
			if ri == 0 {
				c = renderer.Styled(bold, c)
			}
			cells[i] = strings.Split(ansi.Wrap(c, widths[i], ""), "\n")
			h = max(h, len(cells[i]))
		}
		for y := range h {
			parts := make([]string, n)
			for i := range n {
				l := ""
				if y < len(cells[i]) {
					l = cells[i][y]
				}
				parts[i] = pad(l, widths[i], align[i])
			}
			out = append(out, strings.Join(parts, sep))
		}
		if ri == 0 {
			bars := make([]string, n)
			for i, wd := range widths {
				bars[i] = strings.Repeat("─", wd)
			}
			out = append(out, renderer.Styled(dim, strings.Join(bars, "─┼─")))
		}
	}
	return strings.Join(append(out, rest...), "\n")
}

func sum(xs []int) (n int) {
	for _, x := range xs {
		n += x
	}
	return n
}

func widest(xs []int) (i int) {
	for j, x := range xs {
		if x > xs[i] {
			i = j
		}
	}
	return i
}

// pad fills s with spaces to w columns, aligned left, right, or centered.
func pad(s string, w int, align byte) string {
	gap := max(w-ansi.StringWidth(s), 0)
	switch align {
	case 'r':
		return strings.Repeat(" ", gap) + s
	case 'c':
		return strings.Repeat(" ", gap/2) + s + strings.Repeat(" ", gap-gap/2)
	}
	return s + strings.Repeat(" ", gap)
}

func link(z renderer.Zone, w int) string {
	u, text := z.Close[1], renderer.Styled(under, z.Inner(w))
	if u != "" && ansi.Strip(text) != u {
		text += renderer.Styled(dim, " ("+u+")")
	}
	return hyperlink(u, text)
}

// url is a link written as is; the punctuation that ends a sentence after it isn't part of it.
func url(z renderer.Zone, _ int) string {
	if len(z.Open) > 1 {
		return hyperlink(z.Open[1], renderer.Styled(under, z.Open[1]))
	}
	u := strings.TrimRight(z.Open[0], ".,;:!?'\"")
	return hyperlink(u, renderer.Styled(under, u)) + z.Open[0][len(u):]
}

// hyperlink makes s a link to u the terminal opens (OSC 8): in Alacritty, a click with Shift while the mouse
// is the app's. u can't end the sequence early: the core dropped its control characters.
func hyperlink(u, s string) string {
	if u == "" {
		return s
	}
	return ansi.SetHyperlink(u) + s + ansi.ResetHyperlink()
}

func emoji(z renderer.Zone, _ int) string {
	if e, ok := emojis.Get(z.Open[1]); ok {
		return string(e.Unicode)
	}
	return z.Open[0]
}

func tag(z renderer.Zone, w int) string {
	switch z.Open[1] {
	case "sup":
		return mapped(z.Inner(w), sup)
	case "sub":
		return mapped(z.Inner(w), sub)
	}
	return renderer.Styled(tags[z.Open[1]], z.Inner(w))
}

func img(z renderer.Zone, _ int) string {
	alt, src := "image", ""
	for _, m := range attr.FindAllStringSubmatch(z.Open[0], -1) {
		if m[1] == "alt" {
			alt = m[2]
		} else {
			src = m[2]
		}
	}
	return hyperlink(src, renderer.Styled(under, alt)+renderer.Styled(dim, " ("+src+")"))
}

func scripts(from, to string) map[rune]rune {
	f, t := []rune(from), []rune(to)
	m := make(map[rune]rune, len(f))
	for i, r := range f {
		m[r] = t[i]
	}
	return m
}

// script writes a zone in Unicode super- or subscripts, or as written if some character has none.
func script(m map[rune]rune) func(renderer.Zone, int) string {
	return func(z renderer.Zone, _ int) string {
		if s := mapped(z.Text, m); z.Text != "" && s != z.Text {
			return s
		}
		return z.Open[0] + z.Text + z.Close[0]
	}
}

// mapped is s in the script of m; s itself if some character has none.
func mapped(s string, m map[rune]rune) string {
	out := []rune(s)
	for i, r := range out {
		if out[i] = m[r]; out[i] == 0 {
			return s
		}
	}
	return string(out)
}
