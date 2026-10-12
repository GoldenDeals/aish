package math

import (
	"math"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	contract "github.com/GoldenDeals/aish-core/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

func draw(s string) (string, int) {
	b := renderer.New(contract.Markdown)
	b.Write(s)
	_, h := b.Size(40, math.MaxInt)
	scr := uv.NewWindow(40, max(h, 1), nil)
	b.Draw(scr, scr.Bounds())
	return scr.String(), h
}

func TestMath(t *testing.T) {
	if _, h := draw(`$$\frac{a}{b}$$`); h != 3 {
		t.Errorf("display fraction: %d rows", h)
	}
	for in, want := range map[string]string{
		`x $\frac{a}{b}$ y`: "x a/b y",
		`$x^2$`:             "x²",
		"costs $5 and $10":  "costs $5 and $10",
		`$\frac{$`:          `$\frac{$`,
		"$$\\frac{a}{b}":    `$$\frac{a}{b}`, // not closed yet: the source
	} {
		if got, _ := draw(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
