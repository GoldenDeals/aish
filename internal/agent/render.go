package agent

import (
	"io"
	"os"

	"golang.org/x/term"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/markdown"
)

// flusher is the assistant's text output: Flush ends it at the start of a line.
type flusher interface {
	io.Writer
	Flush()
}

// newMarkdown renders the assistant's markdown onto w when out is a
// terminal, and passes it through as is otherwise.
func newMarkdown(w, out io.Writer, cfg config.Config) flusher {
	f, ok := out.(*os.File)
	if !cfg.Markdown || !ok || !term.IsTerminal(int(f.Fd())) {
		return &plain{w: w}
	}
	size := func() (int, int) {
		c, r, err := term.GetSize(int(f.Fd()))
		if err != nil || c <= 0 {
			return 80, 24
		}
		return c, r
	}
	return markdown.New(w, size, cfg.CodeStyle)
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
