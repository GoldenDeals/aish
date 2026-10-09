package proxy

import (
	"context"
	"sync"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
)

// TimesAnswers tells the agent that the proxy keeps the time of its
// questions: see askClock.
func (*ui) TimesAnswers() {}

// askClock is the time an open question, Yes/No or the form of ask_user,
// has left to be answered (agent.AnswerTime). It stands while the
// question is out of sight, under the Ctrl+O viewer or the panes: the user
// reading the outputs there has not gone away. Under the viewer, only as
// long as the user is seen there: a viewer without a key for askIdle was
// left, and the clock goes on under it, standing again on the next key.
// Not for ever, though, keys or not: once the clock has stood askPause in
// all, it goes on under the viewer for good. Once it is out, the question
// ends as at a deadline of its ctx, under the viewer or not.
//
// The question knows when it goes out of sight, as it opens under the
// viewer or opens the viewer on Ctrl+O, and calls sync. Whoever takes the
// viewer away does not tell it (a key, restoreScreen), nor does a key in
// the viewer, so the clock looks every askLook while the question is out
// of sight: standing, whether the question is back or the viewer left;
// going under a viewer left, whether the user is back in it. The user
// gets that much more, or less, and that much less of askPause.
type askClock struct {
	mu     *sync.Mutex      // p.mu, which the timer takes
	hidden func() bool      // whether the question is out of sight; under mu
	seen   func() time.Time // when the user was last seen at what hides it; under mu
	why    error            // the cause the question ends with

	left  time.Duration // the time to answer, as of at
	pause time.Duration // how much longer the clock may stand, as of at
	at    time.Time     // when the clock went on or stood; zero before sync
	going bool          // whether it goes or stands since at
	timer *time.Timer   // of the time out, or of the next look
	gen   int           // the timer's: an older one finds itself stale
	out   chan struct{} // closed when the time is out
	done  bool          // out, or stopped
}

// askLook is how often the clock looks while its question is out of
// sight: whether it is in sight again, or the user gone from the viewer
// over it, or back. A variable: the tests shorten it.
var askLook = 50 * time.Millisecond

// askPause is how long the clock of a question may stand in all, however
// many times the viewer covers it. Not a share of the question's own time:
// the 15 seconds a question gets after a run of unanswered ones are no
// time to read the outputs, and that is the question a user back reads
// them for. Ten minutes, as long as the policy's first question waits
// (askFirst of the agent): time enough to read. Not half an hour: the
// panes keep no time of the user's keys (coverSeen), so every question
// opening under them left open stands that long, and a run of them, down
// to 15 seconds each once nobody answers, would go at half an hour a
// question. A variable: the tests shorten it.
var askPause = 10 * time.Minute

// askIdle is how long the viewer over a question may go without a key
// before the user counts as gone from it and the clock goes on under it.
// Without it every question opening under a viewer left open stood
// askPause, a run of unanswered ones at ten minutes a question instead of
// down to 15 seconds; with it a viewer left costs askIdle once, to the
// question open when the user went, and those after it open under a
// viewer already left. Two minutes: longer than a page of the viewer takes
// to read, or a stack trace on it to think over, so that the question does
// not go while the user reads; and a reader who sits on one page longer
// than that loses only the time till his next key, which has the clock
// stand again. Not as long as askPause: a viewer left would cost as much
// as before. A variable: the tests shorten it.
var askIdle = 2 * time.Minute

// answerClock starts the clock of the question opening on ctx: nil if the
// agent gave it no time to keep, which is a question waiting as long as
// ctx. Called under p.mu, the question on the screen or held.
func (p *Proxy) answerClock(ctx context.Context) *askClock {
	d, why, ok := agent.AnswerTime(ctx)
	if !ok {
		return nil
	}
	c := &askClock{mu: &p.mu, hidden: p.holding, seen: p.coverSeen, why: why, left: d, pause: askPause, out: make(chan struct{})}
	c.sync()
	return c
}

// coverSeen is when the user was last seen at what covers the question:
// the last key in the viewer, or its opening. The panes open by themselves
// and keep no time of keys: they count as watched now, the clock standing
// under them up to askPause. Called under p.mu.
func (t *console) coverSeen() time.Time {
	if t.view != nil {
		return t.view.keyAt
	}
	return time.Now()
}

// sync takes the time since the clock went on or stood off what was left
// of it, then has it stand if the question is out of sight with the user
// there and it may stand yet, and go if not. Called under mu.
func (c *askClock) sync() {
	if c == nil || c.done {
		return
	}
	now := time.Now()
	if !c.at.IsZero() {
		if c.going {
			c.left -= now.Sub(c.at)
		} else {
			c.pause -= now.Sub(c.at)
		}
	}
	c.at = now
	hidden := c.hidden()
	gone := hidden && now.Sub(c.seen()) >= askIdle // from the viewer over it
	c.going = c.pause <= 0 || !hidden || gone
	switch {
	case !c.going:
		c.arm(min(askLook, c.pause))
	case gone && c.pause > 0:
		c.arm(min(askLook, max(c.left, 0))) // a key there has it stand again
	default:
		c.arm(max(c.left, 0))
	}
}

func (c *askClock) arm(d time.Duration) {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.gen++
	gen := c.gen
	c.timer = time.AfterFunc(d, func() { c.tick(gen) })
}

// tick is the time out, or the clock looking again while the question is
// out of sight. Gone out of sight with nobody telling, the question stands
// with what was left, as long as the clock may stand yet; the viewer over
// it left, it goes on.
func (c *askClock) tick(gen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done || gen != c.gen {
		return
	}
	c.sync()
	if c.going && c.left <= 0 {
		c.stop()
		close(c.out)
	}
}

// stop is the question ended, answered or not. Called under mu.
func (c *askClock) stop() {
	if c == nil || c.done {
		return
	}
	c.done = true
	c.timer.Stop()
}

// ranOut is closed when the time is out; never without a clock.
func (c *askClock) ranOut() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.out
}

// ended is why the question on ctx ended unanswered: the error it returns
// and the cause it shows. The end of the request comes first.
func (c *askClock) ended(ctx context.Context) (err, why error) {
	if ctx.Err() != nil || c == nil {
		return ctx.Err(), context.Cause(ctx)
	}
	return context.DeadlineExceeded, c.why
}
