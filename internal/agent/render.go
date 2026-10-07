package agent

import (
	"io"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/markdown"
)

// flusher is the assistant's text output: Flush ends it at the start of a line.
type flusher interface {
	io.Writer
	Flush()
}

// newMarkdown renders the assistant's markdown onto w when size reports a
// terminal, and passes it through as is otherwise.
func newMarkdown(w io.Writer, size func() (cols, rows int), cfg config.Config) flusher {
	if cols, _ := size(); !cfg.Markdown || cols <= 0 {
		return &plain{w: w}
	}
	return markdown.New(w, func() (int, int) {
		c, r := size()
		if c <= 0 {
			return 80, 24
		}
		return c, r
	}, cfg.CodeStyle)
}

type plain struct {
	w   io.Writer
	any bool
	bol bool
}

func (p *plain) Write(b []byte) (int, error) {
	if len(b) > 0 {
		p.any, p.bol = true, b[len(b)-1] == '\n'
	}
	return p.w.Write(b)
}

func (p *plain) Flush() {
	if p.any && !p.bol {
		io.WriteString(p.w, "\n")
	}
	p.any = false
}
