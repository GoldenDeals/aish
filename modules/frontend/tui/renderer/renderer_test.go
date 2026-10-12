package renderer

import (
	"math"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	contract "github.com/GoldenDeals/aish-core/contracts/tui/renderer"
)

// tag draws a zone as name(inner), so a test sees which rule took what.
func tag(name string) func(Zone, int) string {
	return func(z Zone, w int) string { return name + "(" + z.Inner(w) + ")" }
}

// with is a Block parsed by rs.
func with(rs ...Rule) *block {
	b := &block{}
	for _, r := range rs {
		b.rules = append(b.rules, compile(r))
	}
	return b
}

func draw(b contract.Block, w int) string {
	_, h := b.Size(w, math.MaxInt)
	scr := uv.NewWindow(w, max(h, 1), nil)
	b.Draw(scr, scr.Bounds())
	return scr.String()
}

var md = []Rule{
	{Open: `^(#{1,6}) +`, Close: `\n`, Nested: true, Draw: tag("h")},
	{Open: `\*\*`, Close: `\*\*`, Abort: `\n`, Nested: true, Draw: tag("b")},
	{Open: `\*`, Close: `\*`, Abort: `\n`, Nested: true, Draw: tag("i")},
	{Open: "`", Close: "`", Abort: `\n`, Draw: tag("c")},
	{Open: "^```\\n", Close: "^```", Draw: tag("fence")},
	{Open: "^```(\\w+)[^\\n]*\\n", Close: "^```", Draw: func(z Zone, _ int) string { return z.Open[1] + "(" + z.Text + ")" }},
	{Open: "^```mermaid\\n", Close: "^```", Draw: tag("mermaid")},
}

func TestRules(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"# Today is *good* day\nnext", "h(Today is i(good) day)\nnext"}, // the heading leaves its \n
		{"**a *b* c**", "b(a i(b) c)"},                                   // the Close wins a tie, nesting
		{"*a **b** c*", "i(a b(b) c)"},                                   // the more specific Open wins
		{"**b *i***", "b(b i(i))"},                                       // *** doesn't open: it closes
		{"***x***", "b(i(x))"},                                           // an Open that doesn't close gives way
		{"**a\nb", "**a\nb"},                                             // aborted: text, ** not an empty italic
		{"*x", "*x"},                                                     // unclosed with Abort: text
		{"`**`", "c(**)"},                                                // raw: not parsed
		{"# \nx", "h()\nx"},                                              // no Abort: a zone with no text is one
		{"a # b", "a # b"},                                               // ^ is a line start only
		{"```\nx\n```", "fence(x\n)"},                                    // bare fence
		{"```go\nx\n```", "go(x\n)"},                                     // more literal characters
		{"```mermaid\nx\n```", "mermaid(x\n)"},                           // even more
		{"```go\nx\n", "go(x\n)"},                                        // unclosed without Abort: kept
		{"a\x1b[31mb\tc\u009b", "a[31mb    c"},                           // control characters dropped
	} {
		b := with(md...)
		b.Write(c.in)
		if got := draw(b, 40); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestText(t *testing.T) {
	b := New(contract.Text)
	b.Write("**not bold** one two three")
	if got, want := draw(b, 10), "**not\nbold** one\ntwo three"; got != want { // as is, wrapped
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStream(t *testing.T) {
	const in = "# T *i*\n\n**b** `c`\n```go\nfmt\n```\n**x\n"
	whole := with(md...)
	whole.Write(in)
	b := with(md...)
	for i := range len(in) {
		b.Write(in[i : i+1])
		b.Size(40, math.MaxInt) // a frame between chunks
	}
	if got, want := draw(b, 40), draw(whole, 40); got != want {
		t.Errorf("streamed %q, whole %q", got, want)
	}
}

func TestPanic(t *testing.T) {
	b := with(Rule{Open: `!`, Close: `!`, Draw: func(Zone, int) string { panic("lib") }})
	b.Write("a !x! b")
	if got, want := draw(b, 20), "a !x! b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStyled(t *testing.T) {
	b := with(
		Rule{Open: `\*\*`, Close: `\*\*`, Nested: true, Draw: func(z Zone, w int) string { return Styled(uv.Style{Attrs: uv.AttrBold}, z.Inner(w)) }},
		Rule{Open: `_`, Close: `_`, Draw: func(z Zone, w int) string { return Styled(uv.Style{Attrs: uv.AttrItalic}, z.Text) }},
	)
	b.Write("**a _b_ c**")
	scr := uv.NewWindow(10, 1, nil)
	b.Draw(scr, scr.Bounds())
	if c := scr.CellAt(2, 0); c.Content != "b" || c.Style.Attrs != uv.AttrBold|uv.AttrItalic {
		t.Errorf("b: %+v", c)
	}
	if c := scr.CellAt(4, 0); c.Content != "c" || c.Style.Attrs != uv.AttrBold { // bold again after the italic's reset
		t.Errorf("c: %+v", c)
	}
}

func TestLiterals(t *testing.T) {
	for pat, want := range map[string]int{"```mermaid\\n": 11, "```(\\w+)[^\\n]*\\n": 4, `\*\*`: 2, `-{3,}|\*{3,}`: 3, `a+b?`: 1} {
		if got := newExpr(pat).spec; got != want {
			t.Errorf("%s: %d, want %d", pat, got, want)
		}
	}
}

func TestKind(t *testing.T) {
	Kind("upper", func(src string, w int) string {
		if src == "panic" {
			panic("choked")
		}
		return "[" + src + "]"
	})
	defer delete(kinds, "upper")
	b := New("upper")
	b.Write("a\x1b[31mb")
	if got := draw(b, 10); got != "[a[31mb]" { // the drawer gets the text without control characters
		t.Errorf("kind: %q", got)
	}
	b = New("upper")
	b.Write("panic")
	if got := draw(b, 10); got != "panic" {
		t.Errorf("a drawer that panics: %q", got)
	}
	b = New("unknown")
	b.Write("**a**")
	if got := draw(b, 10); got != "**a**" {
		t.Errorf("a kind nobody draws: %q", got)
	}
}
