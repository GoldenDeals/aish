// Package plaintext renders the Text kind, the fallback for every kind without a renderer: the content as is,
// wrapped at word boundaries. Control characters are dropped, so the content can't talk to the terminal.
package plaintext

import (
	"context"
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

type mod struct{ b bus.Bus }

func init() { module.Register("tui.renderer.plaintext", &mod{}) }

func (m *mod) Init(b bus.Bus, _ config.Config) error {
	m.b = b
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	return m.b.Dispatch(ctx, event.New(renderer.Announce, renderer.Renderer{Kind: renderer.Text, New: New}))
}

func (m *mod) Stop(context.Context) error { return nil }

// New returns an empty plain text Block.
func New(renderer.Pick) renderer.Block { return &block{} }

type block struct {
	text  strings.Builder
	width int
	lines []string // text wrapped at width; nil after Write
}

func (b *block) Write(chunk string) {
	b.text.WriteString(clean(chunk))
	b.lines = nil
}

func (b *block) Size(width, _ int) (int, int) { return width, len(b.wrap(width)) }

func (b *block) Draw(scr uv.Screen, area uv.Rectangle) {
	vis := area.Intersect(scr.Bounds())
	for i, l := range b.wrap(area.Dx()) {
		if line := uv.Rect(area.Min.X, area.Min.Y+i, area.Dx(), 1); line.Overlaps(vis) {
			uv.NewStyledString(l).Draw(scr, line) // clean left no escape sequences for it to read
		}
	}
}

func (b *block) wrap(width int) []string {
	if b.lines == nil || b.width != width {
		b.width, b.lines = width, nil
		if s := b.text.String(); s != "" {
			b.lines = strings.Split(ansi.Wrap(s, width, ""), "\n")
		}
	}
	return b.lines
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
