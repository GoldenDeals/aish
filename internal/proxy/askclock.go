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
// reading the outputs there has not gone away. Not for ever, though: the
// user may have gone with the viewer open, so once the clock has stood
// askPause in all, it goes on under the viewer too. Once it is out, the
// question ends as at a deadline of its ctx, under the viewer or not.
//
// The question knows when it goes out of sight, as it opens under the
// viewer or opens the viewer on Ctrl+O, and calls sync. Whoever takes the
// viewer away does not tell it (a key, restoreScreen), so the clock
// standing looks every askLook whether the question is back: the user
// gets that much more, and that much less of askPause.
type askClock struct {
	mu     *sync.Mutex // p.mu, which the timer takes
	hidden func() bool // whether the question is out of sight; under mu
	why    error       // the cause the question ends with

	left  time.Duration // the time to answer, as of at
	pause time.Duration // how much longer the clock may stand, as of at
	at    time.Time     // when the clock went on or stood; zero before sync
	going bool          // whether it goes or stands since at
	timer *time.Timer   // of the time out, or of the next look
	gen   int           // the timer's: an older one finds itself stale
	out   chan struct{} // closed when the time is out
	done  bool          // out, or stopped
}

// askLook is how often the clock standing looks whether its question is in
// sight again. A variable: the tests shorten it.
var askLook = 50 * time.Millisecond

// askPause is how long the clock of a question may stand in all, however
// many times the viewer covers it. Not a share of the question's own time:
// the 15 seconds a question gets after a run of unanswered ones are no
// time to read the outputs, and that is the question a user back reads
// them for. Ten minutes, as long as the policy's first question waits
// (askFirst of the agent): time enough to read. Not half an hour: every
// question opening under a viewer left open stands that long, and a run
// of them, down to 15 seconds each once nobody answers, would go at half
// an hour a question. A variable: the tests shorten it.
var askPause = 10 * time.Minute

// answerClock starts the clock of the question opening on ctx: nil if the
// agent gave it no time to keep, which is a question waiting as long as
// ctx. Called under p.mu, the question on the screen or held.
func (p *Proxy) answerClock(ctx context.Context) *askClock {
	d, why, ok := agent.AnswerTime(ctx)
	if !ok {
		return nil
	}
	c := &askClock{mu: &p.mu, hidden: p.holding, why: why, left: d, pause: askPause, out: make(chan struct{})}
	c.sync()
	return c
}

// sync takes the time since the clock went on or stood off what was left
// of it, then has it stand if the question is out of sight and it may
// stand yet, and go if not. Called under mu.
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
	c.going = c.pause <= 0 || !c.hidden()
	if c.going {
		c.arm(max(c.left, 0))
	} else {
		c.arm(min(askLook, c.pause))
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

// tick is the time out, or the clock standing looking again. Gone out of
// sight with nobody telling, the question stands with what was left, as
// long as the clock may stand yet.
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
