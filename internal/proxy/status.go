package proxy

import (
	"fmt"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// statusText is the context size, the profile when it is not the one
// config.toml selects (config.Root for its top level), the model and its
// effort, colored by how full the context is.
func (p *Proxy) statusText() (text, color string) {
	defer func() { text = p.yoloStatus(text) }() // last, whatever the rest is
	size, cfg := p.contextSize(p.sess.Entries())
	tokens, window := size.Tokens, cfg.ContextWindow
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
	if window > 0 {
		pct := tokens * 100 / window
		ctx += fmt.Sprintf("/%s %d%%", session.Short(window), pct)
		switch {
		case pct >= 90:
			color = "\x1b[31m"
		case pct >= 70:
			color = "\x1b[33m"
		}
		// The agent compacts before its next turn, not while the shell is
		// the user's: no turn of the model is spent on that.
		if limit := agent.CompactLimit(cfg); limit > 0 && tokens > limit {
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
//
// The prompt and the command need the room more: 40 columns are theirs,
// past what is drawn. While yolo is on, its mark stays with prompt_status
// off and in a terminal too narrow for the whole status: alone, on the
// same terms, so not below 44 columns.
func (p *Proxy) drawStatus() {
	p.dropLine()
	if p.size == nil || p.col.off {
		return
	}
	w, h := p.size()
	var text, color string
	if p.promptStatus {
		text, color = p.statusText()
	}
	if !p.promptStatus || w < runewidth.StringWidth(text)+40 {
		// "" while yolo is off; paintYolo colors the mark.
		if text, color = p.yoloStatus(""), ""; text == "" || w < runewidth.StringWidth(text)+40 {
			return
		}
	}
	p.line = newInputLine(w, h, text, color)
	p.paintYolo(p.line)
	p.emit(p.line.draw())
}

// info is what `aish` commands ask the proxy about the shell. Called under
// p.mu.
func (p *Proxy) info() rpc.Info {
	return rpc.Info{SessionID: p.sess.ID, Dir: p.sess.Dir(), Saved: p.sess.Saved(), Name: p.sess.Name(),
		Profile: p.profile, Model: p.model, Effort: p.effort, Window: p.window, Yolo: p.yolo, Asking: p.asking}
}

// status counts what `aish status` shows. Called under p.mu.
func (p *Proxy) status() rpc.Status {
	all := p.sess.Entries()
	es := session.Current(all)
	st := rpc.Status{Info: p.info(), ProjectConfig: p.project, Overhead: p.overhead}
	size, _ := p.contextSize(all)
	st.Tokens, st.Measured = size.Tokens, size.Measured
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
		}
	}
	return st
}
