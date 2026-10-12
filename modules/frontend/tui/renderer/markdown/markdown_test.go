package markdown

import (
	"math"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	contract "github.com/GoldenDeals/aish-core/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

func draw(s string, w int) (string, *uv.Window) {
	b := renderer.New(contract.Markdown)
	b.Write(s)
	_, h := b.Size(w, math.MaxInt)
	scr := uv.NewWindow(w, max(h, 1), nil)
	b.Draw(scr, scr.Bounds())
	return scr.String(), scr
}

func TestBlocks(t *testing.T) {
	in := strings.Join([]string{
		"# Title *it*",
		"## Sub",
		"- a **b**",
		"  - nested",
		"3. third",
		"- [x] done",
		"- [ ] todo",
		"> quote `c`",
		">",
		">> deeper",
		"---",
		"```",
		"plain code",
		"```",
		"[^1]: note",
		": definition",
		"",
		"| A | Right |",
		"|---|------:|",
		"| x | 1 |",
		"| long | 22 |",
		"",
		"end",
	}, "\n")
	want := strings.Join([]string{
		"Title it",
		"Sub",
		"• a b",
		"  • nested",
		"3. third",
		"☑ done",
		"☐ todo",
		"│ quote c",
		"│ ", // the bar's space is styled: kept
		"│ │ deeper",
		"────────────────────",
		"  plain code",
		"[1] note",
		"  definition",
		"",
		"A    │ Right",
		"─────┼──────",
		"x    │     1",
		"long │    22",
		"",
		"end",
	}, "\n")
	if got, _ := draw(in, 20); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestInline(t *testing.T) {
	for in, want := range map[string]string{
		"**b** *i* ***bi*** __b__ _i_ snake_case_name": "b i bi b i snake_case_name",
		"~~s~~ ==m== ++u++ x^2^ H~2~O 2^10":            "s m u x² H₂O 2^10",
		"`a*b*` ``x`y`` `` `k` ``":                     "a*b* x`y `k`",
		"[text](https://x.io) ![alt](p.png) [x]":       "text (https://x.io) alt (p.png) [x]",
		"see https://x.io. or <https://y.io>":          "see https://x.io. or https://y.io",
		`\*no\* [^1] :rocket: :nope: 10:30:45`:         "*no* [1] 🚀 :nope: 10:30:45",
		"<kbd>Ctrl</kbd> <b>B</b> a<br>b <!-- hid -->": "Ctrl B a\nb",
		`<img src="p.png" alt="pic"> <div>x</div>`:     "pic (p.png) x",
		"2 * 3 = 6 and **unclosed":                     "2 * 3 = 6 and **unclosed",
	} {
		if got, _ := draw(in, 60); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestStyles(t *testing.T) {
	_, scr := draw("**b *i*** `c`", 20)
	for x, want := range map[int]uint8{0: uv.AttrBold, 2: uv.AttrBold | uv.AttrItalic} {
		if c := scr.CellAt(x, 0); c.Style.Attrs != want {
			t.Errorf("cell %d %q: attrs %b, want %b", x, c.Content, c.Style.Attrs, want)
		}
	}
	if c := scr.CellAt(4, 0); c.Content != "c" || c.Style.Fg == nil {
		t.Errorf("code span: %+v", c)
	}
	_, scr = draw("[a](https://x.io) https://y.io", 40)
	for x, want := range map[int]string{0: "https://x.io", 2: "https://x.io", 19: "https://y.io"} { // the text, its url, a bare url
		if c := scr.CellAt(x, 0); c.Link.URL != want {
			t.Errorf("cell %d %q: link %q, want %q", x, c.Content, c.Link.URL, want)
		}
	}
}

func TestStream(t *testing.T) {
	const in = "# T\n- a **b** `c`\n| A | B |\n|---|---|\n| 1 | 2 |\n\n```\nx\n```\nend *i*"
	whole, _ := draw(in, 30)
	b := renderer.New(contract.Markdown)
	for i := range len(in) {
		b.Write(in[i : i+1])
		b.Size(30, math.MaxInt)
	}
	_, h := b.Size(30, math.MaxInt)
	scr := uv.NewWindow(30, h, nil)
	b.Draw(scr, scr.Bounds())
	if got := scr.String(); got != whole {
		t.Errorf("streamed %q, whole %q", got, whole)
	}
}
