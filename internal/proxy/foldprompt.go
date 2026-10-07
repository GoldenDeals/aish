package proxy

import (
	"slices"
	"time"
)

// A command the agent runs may wait for input: its stdin is /dev/null, but
// sudo, ssh and git ask on /dev/tty. Its output is folded, all of it with
// fold_lines = 0, the default, or with hide_work, and the prompt would be
// hidden like the rest: the user would see the call and its status, or the
// agent's line turning, while the command waits. Two things tell it, and
// either opens the command's fold as a full-screen program does: the output
// stands still on a hidden line with text (fold.prompts), which needs no
// one to guess that the command waits, and the user typing, the keys going
// to the command, which shows a prompt the first misses, one ended by a
// newline or drawn over a line. A hidden command's line of calls looks for
// the first as it turns (spinFrame); the fold of another has a promptWatch.

// promptWatch watches the fold of the agent's command the shell runs for a
// prompt the fold hides, until the command is over.
type promptWatch struct {
	fold  *fold
	timer *time.Timer
}

// watchFold watches f until stopWatch. Called under p.mu.
func (p *Proxy) watchFold(f *fold) {
	p.stopWatch()
	w := &promptWatch{fold: f}
	w.timer = time.AfterFunc(promptWait, func() { p.watchTick(w) })
	p.watch = w
}

// stopWatch stops watching: the fold is not opened after it. Called under
// p.mu.
func (r *recorder) stopWatch() {
	if r.watch != nil {
		r.watch.timer.Stop()
		r.watch = nil
	}
}

// watchTick opens the fold of w if its command waits for input, unless w
// was stopped; if not, it looks again when the output, unless more comes,
// will have stood still for promptWait.
func (p *Proxy) watchTick(w *promptWatch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watch != w {
		return
	}
	now := time.Now()
	switch {
	case w.fold.open:
		p.watch = nil // by a full-screen program
	case w.fold.prompts(now):
		p.showHidden(w.fold)
	default:
		w.timer.Reset(w.fold.promptDue(now))
	}
}

// typedHidden shows the command the keys b go to, if its fold hides what it
// prints: the user has to see what they answer. Not for the keys that
// signal it, Ctrl+C, Ctrl+\ and Ctrl+Z: it is cut short or stopped, and its
// fold stays as it was. Called under p.mu.
func (p *Proxy) typedHidden(b []byte) {
	f := p.hidingFold()
	if f == nil || !slices.ContainsFunc(b, isInput) {
		return
	}
	p.showHidden(f)
}

func isInput(c byte) bool { return c != 0x03 && c != 0x1c && c != 0x1a }

// hidingFold is the fold of the agent's command the shell runs, if it hides
// what the command prints now. Called under p.mu.
func (r *recorder) hidingFold() *fold {
	var f *fold
	switch {
	case r.spin != nil:
		f = r.spin.fold
	case r.watch != nil:
		f = r.watch.fold
	}
	if f == nil || !f.hides() {
		return nil
	}
	return f
}

// showHidden opens f, the fold of a command that waits for input: below the
// call, or the agent's line of calls, which stops turning, go the end of
// what the command printed and then all it prints. The agent, back, goes on
// below. Called under p.mu.
func (p *Proxy) showHidden(f *fold) {
	p.stopSpin()
	p.stopWatch()
	p.emit(f.expand())
}
