package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
)

// lookupWindow asks prov, the provider of profile, for the context size of
// model, which the models list reports for Anthropic. The shell may have
// switched to another profile or model by the time it answers.
func (p *Proxy) lookupWindow(prov llm.Provider, profile, model string) {
	if prov == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ms, err := prov.Models(ctx)
	if err != nil {
		return
	}
	for _, m := range ms {
		if m.ID == model && m.Window > 0 {
			p.mu.Lock()
			if p.profile == profile && p.model == model && p.window == 0 {
				p.window = m.Window
			}
			p.mu.Unlock()
		}
	}
}

// statusText is the context size, the profile when it is not the one
// config.toml selects (config.Root for its top level), the model and its
// effort, colored by how full the context is.
func (p *Proxy) statusText() (text, color string) {
	tokens := session.Tokens(p.sess.Entries(), p.maxOutput)
	text, color = p.model, "\x1b[2m"
	if p.profile != p.defProfile {
		prof := p.profile
		if prof == "" {
			prof = config.Root
		}
		text = prof + " · " + text
	}
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
		// The agent compacts before its next turn, not while the shell is
		// the user's: no turn of the model is spent on that.
		if limit := int(p.compactAt * float64(p.window)); limit > 0 && tokens > limit {
			ctx += " compact?"
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
