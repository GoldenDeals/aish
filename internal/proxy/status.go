package proxy

import (
	"fmt"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// statusText is the context size, the profile when it is not the one
// config.toml selects (config.Root for its top level), the model and its
// effort, colored by how full the context is.
func (p *Proxy) statusText() (text, color string) {
	defer func() { text = p.yoloStatus(text) }() // last, whatever the rest is
	tokens, _ := p.contextTokens(p.sess.Entries())
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
// never know: no PS1 to keep in sync with prompts that rebuild it. Till
// the next command, p.line keeps it off the line being typed, and left of
// what the shell prints at the right of the prompt, zsh's RPROMPT. A
// prompt the command's output left off the first column gets none: p.line
// counts its columns from 0.
func (p *Proxy) drawStatus() {
	p.dropLine()
	if !p.promptStatus || p.size == nil || p.col.off {
		return
	}
	text, color := p.statusText()
	w, h := p.size()
	width := runewidth.StringWidth(text)
	if w < width+40 {
		return // the prompt and the command need the room more
	}
	p.line = newInputLine(w, h, text, color)
	p.paintYolo(p.line)
	p.emit(p.line.draw())
}

// info is what `aish` commands ask the proxy about the shell. Called under
// p.mu.
func (p *Proxy) info() rpc.Info {
	return rpc.Info{SessionID: p.sess.ID, Dir: p.sess.Dir(), Saved: p.sess.Saved(),
		Profile: p.profile, Model: p.model, Effort: p.effort, Window: p.window, Asking: p.asking}
}

// status counts what `aish status` shows. Called under p.mu.
func (p *Proxy) status() rpc.Status {
	all := p.sess.Entries()
	es := session.Current(all)
	st := rpc.Status{Info: p.info(), ProjectConfig: p.project}
	st.Tokens, _ = p.contextTokens(es)
	for _, e := range all {
		switch e.Kind {
		case session.KindSummary:
			st.Compacts++
		case session.KindAssistant:
			st.ToolCalls += len(e.ToolCalls)
			st.InputTokens += e.InputTokens
			st.CachedTokens += e.CachedTokens
			st.OutputTokens += e.OutputTokens
		}
	}
	for _, e := range es {
		switch e.Kind {
		case session.KindShell:
			st.Commands++
		case session.KindUser:
			st.Requests++
		case session.KindAssistant:
			st.Measured = st.Measured || e.InputTokens > 0
		}
	}
	return st
}
