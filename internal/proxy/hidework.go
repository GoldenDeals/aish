package proxy

import (
	"slices"
	"time"

	"github.com/inebotov/aish/internal/agent"
)

// With hide_work the agent hands the shell a command and returns, its line
// of calls left open, the last on the screen: the command is folded
// quietly (fold.quiet) and nothing of it is drawn. Nobody would draw that
// line until the agent goes on, and a long command would look like aish
// hung; so the proxy keeps it turning, as the agent's spinner would, until
// the agent writes again or the command is over.

// The proxy's ui must stay a Hider: the agent finds it out at run time,
// and hide_work would be off without a word.
var _ agent.Hider = (*ui)(nil)

// spinTick is how often the line turns: as the agent's spinner does.
const spinTick = 80 * time.Millisecond

// spin is the agent's line of calls the proxy keeps turning.
type spin struct {
	line  func(n, cols int) string // see agent.Hider
	n     int                      // frames drawn
	fold  *fold                    // the command's, once it runs
	timer *time.Timer
}

// startSpin keeps the agent's line turning, line drawing it, until
// stopSpin. Called under p.mu.
func (p *Proxy) startSpin(line func(n, cols int) string) {
	p.stopSpin()
	if line == nil || p.size == nil {
		return
	}
	s := &spin{line: line}
	s.timer = time.AfterFunc(spinTick, func() { p.spinFrame(s) })
	p.spin = s
}

// stopSpin stops the line turning: no frame is drawn after it. The line
// stays as the last frame left it, the cursor at its end. Called under p.mu.
func (p *Proxy) stopSpin() {
	if p.spin != nil {
		p.spin.timer.Stop()
		p.spin = nil
	}
}

// spinFrame draws the next frame of s in place of the line, unless s was
// stopped. Not while the viewer or the panes have the screen, nor while a
// full-screen program has it: the frames would go in there. A full-screen
// program the command runs opens its fold, and what the command prints
// after it shows below the line, which is not the last any more: s stops.
// So it does when the command waits for input, which s watches for.
func (p *Proxy) spinFrame(s *spin) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spin != s {
		return
	}
	if s.fold != nil && s.fold.open {
		p.spin = nil
		return
	}
	if s.fold != nil && s.fold.prompts(time.Now()) {
		p.showHidden(s.fold)
		return
	}
	if !p.holding() && !p.screen.alt {
		w, _ := p.size()
		s.n++
		p.emit([]byte("\r" + s.line(s.n, w) + "\x1b[K"))
	}
	s.timer.Reset(spinTick)
}

// A hidden command may wait for input: its stdin is /dev/null, but sudo,
// ssh and git ask on /dev/tty, and the prompt would be hidden like the
// rest, the line turning while nothing goes on. Two things tell it, and
// either opens the command's fold as a full-screen program does: the
// output stands still on a line with text (fold.prompts), which needs no
// one to guess that the command waits, and the user typing, the keys going
// to the command, which shows a prompt the first misses, one ended by a
// newline or drawn over a line.

// typedHidden shows the hidden command the keys b go to: the user has to
// see what they answer. Not for the keys that signal it, Ctrl+C, Ctrl+\
// and Ctrl+Z: it is cut short or stopped, and the agent's line waits for
// the prompt as before. Called under p.mu.
func (p *Proxy) typedHidden(b []byte) {
	s := p.spin
	if s == nil || s.fold == nil || s.fold.open || !slices.ContainsFunc(b, isInput) {
		return
	}
	p.showHidden(s.fold)
}

func isInput(c byte) bool { return c != 0x03 && c != 0x1c && c != 0x1a }

// showHidden opens f, the fold of a hidden command that waits for input:
// the agent's line stops turning, and below it go the end of what the
// command printed and then all it prints. The agent, back, goes on with
// its line below. Called under p.mu.
func (p *Proxy) showHidden(f *fold) {
	p.stopSpin()
	p.emit(f.expand())
}
