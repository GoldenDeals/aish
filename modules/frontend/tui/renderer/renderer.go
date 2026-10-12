// Package renderer is the renderer core: it parses content by the rules renderers register and draws it.
//
// A renderer is a package that registers its rules from init. A rule claims a zone of the content: the text from
// a match of its Open to the next match of its Close. Zones nest where a rule says so: `# Today is *good* day` is
// a heading zone with an italic zone in it. Where several expressions match at one place, the most specific is
// tried first: the one any match of which has more literal characters, then the longer match; the zone's own Close
// and Abort go before an Open of a tie. An Open whose zone doesn't close gives way to the next. The text no zone
// claims is shown as is, and the whole is wrapped at word boundaries.
// A kind of content that isn't text, a tool call, is drawn whole by the function a renderer registers for it.
//
// Content is untrusted: control characters are dropped before rules see it, so it can't talk to the terminal.
// A renderer that panics on a zone shows the zone's source instead.
package renderer

import (
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	contract "github.com/GoldenDeals/aish-core/contracts/tui/renderer"
)

func init() { contract.Provide(New) }

// rule is what the core asks of a rule.
type rule interface {
	Pattern() (open, close, abort string)
	Nests() bool
	Render(z Zone, width int) string
}

// Rule claims zones for a renderer. Its expressions are regexps where ^ and $ match at line starts and ends.
type Rule struct {
	Open string // where a zone opens; it must not match empty
	// Close is where the zone closes, searched after Open; "" for a zone of the Open match alone.
	// The line breaks ending a match of Close, or of such an Open, stay out of the zone: a heading closes
	// at \n and leaves it to the text around.
	Close  string
	Abort  string                         // found before Close, it leaves Open as text; with it, a zone the text ends in, or one with no text, is text too
	Nested bool                           // the zone's text is parsed for zones too
	Draw   func(z Zone, width int) string // the zone as styled text (SGR, \n); the lines it makes fit width
}

func (r Rule) Pattern() (string, string, string) { return r.Open, r.Close, r.Abort }
func (r Rule) Nests() bool                       { return r.Nested }
func (r Rule) Render(z Zone, width int) string   { return r.Draw(z, width) }

// Zone is a piece of content a rule claimed.
type Zone struct {
	Open  []string // Open's match and submatches
	Close []string // Close's; nil while the text hasn't reached it, and for a rule without Close
	Text  string   // the text between them
	inner func(width int) string
}

// Inner is Text with its zones drawn at width; Text as is for a rule that doesn't nest.
func (z Zone) Inner(width int) string { return z.inner(width) }

var (
	rules []*compiled // in the order registered
	kinds = map[string]func(src string, width int) string{}
)

// Register adds rules; call it from init. A bad expression panics: it would fail on every start.
func Register(rs ...rule) {
	for _, r := range rs {
		rules = append(rules, compile(r))
	}
}

// Kind draws the blocks of kind with draw, instead of parsing them by the rules; call it from init.
// draw gets the block's text so far, control characters dropped, and returns it styled; the lines it makes fit width.
func Kind(kind string, draw func(src string, width int) string) { kinds[kind] = draw }

// Styled is s in st. st opens again after each reset in s, so the styles of zones within s add to it.
func Styled(st uv.Style, s string) string {
	if st.IsZero() {
		return s
	}
	on := st.String()
	s = strings.NewReplacer("\x1b[0m", "\x1b[0m"+on, "\x1b[m", "\x1b[m"+on).Replace(s)
	return on + s + ansi.ResetStyle
}

// Indent is s hard-wrapped to width less n columns, each line after n spaces: a block of code within the text.
func Indent(s string, n, width int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(ansi.Hardwrap(s, max(width-n, 1), true), "\n", "\n"+pad)
}

// New returns an empty Block for content of kind: Markdown is parsed by the rules, a kind with a Kind
// is drawn by it, anything else is shown as is.
func New(kind string) contract.Block {
	b := &block{draw: kinds[kind]}
	if kind == contract.Markdown {
		b.rules = rules
	}
	return b
}

type block struct {
	rules []*compiled
	draw  func(src string, width int) string
	src   strings.Builder
	fresh bool // out is drawn at width from src
	width int
	out   string
	rows  int
	memo  map[key]string // the zones the last layout drew
}

func (b *block) Write(chunk string) {
	b.src.WriteString(clean(chunk))
	b.fresh = false
}

func (b *block) Size(width, _ int) (int, int) {
	b.layout(width)
	return width, b.rows
}

func (b *block) Draw(scr uv.Screen, area uv.Rectangle) {
	b.layout(area.Dx())
	uv.NewStyledString(b.out).Draw(scr, area) // clean left no escape sequences but the renderers' styles
}

// layout parses src again and draws it at width, unless it already is: the text only grows, so a parse
// of all of it is right at every point of a stream.
func (b *block) layout(width int) {
	if b.fresh && b.width == width {
		return
	}
	var out string
	if b.draw != nil {
		out = b.drawn(width)
	} else {
		p := parser{src: b.src.String(), rules: b.rules, last: b.memo, memo: map[key]string{}}
		out, b.memo = p.inner(p.parse(), width), p.memo
	}
	b.out = strings.TrimRight(ansi.Wrap(out, width, ""), "\n")
	b.fresh, b.width, b.rows = true, width, 0
	if b.out != "" {
		b.rows = strings.Count(b.out, "\n") + 1
	}
}

// drawn is the block drawn by its kind's draw, or its text as it came if draw panics on it.
func (b *block) drawn(width int) (out string) {
	defer func() {
		if recover() != nil {
			out = b.src.String()
		}
	}()
	return b.draw(b.src.String(), width)
}

// clean drops control characters but newlines: with ESC the text could move the cursor or send the terminal commands.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\t", "    ") // shortcut: a tab is 4 spaces, not a tab stop; align to stops if tables need it
	return strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
