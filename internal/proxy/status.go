package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/session"
)

// lookupWindow asks the API for the context size of model, which the
// models list reports for Anthropic.
func (p *Proxy) lookupWindow(model string) {
	if p.prov == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ms, err := p.prov.Models(ctx)
	if err != nil {
		return
	}
	for _, m := range ms {
		if m.ID == model && m.Window > 0 {
			p.mu.Lock()
			if p.model == model && p.window == 0 {
				p.window = m.Window
			}
			p.mu.Unlock()
		}
	}
}

// statusText is the context size, the model and its effort, colored by how
// full the context is.
func (p *Proxy) statusText() (text, color string) {
	tokens := session.Tokens(p.sess.Entries(), p.maxOutput)
	text, color = p.model, "\x1b[2m"
	if p.effort != "" {
		text += " · " + p.effort
	}
	if tokens == 0 {
		return text, color
	}
	ctx := session.Short(tokens)
	if p.window > 0 {
		pct := tokens * 100 / p.window
		ctx += fmt.Sprintf("/%s %d%%", session.Short(p.window), pct)
		switch {
		case pct >= 90:
			color = "\x1b[31m"
		case pct >= 70:
			color = "\x1b[33m"
		}
	}
	return ctx + " · " + text, color
}

// drawStatus puts the status at the right edge of the line the prompt is
// about to be printed on. The cursor is put back, so bash and readline
// never know: no PS1 to keep in sync with prompts that rebuild it.
func (p *Proxy) drawStatus() {
	if !p.promptStatus || p.size == nil {
		return
	}
	text, color := p.statusText()
	w, _ := p.size()
	width := runewidth.StringWidth(text)
	if w < width+40 {
		return // the prompt and the command need the room more
	}
	p.emit(fmt.Appendf(nil, "\x1b7\x1b[%dG%s%s\x1b[0m\x1b8", w-width, color, text))
}
