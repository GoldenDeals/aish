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
// reading the outputs there has not gone away. Once it is out, the
// question ends as at a deadline of its ctx.
//
// The question knows when it goes out of sight, as it opens under the
// viewer or opens the viewer on Ctrl+O, and calls sync. Whoever takes the
// viewer away does not tell it (a key, restoreScreen), so the clock
// standing looks every askLook whether the question is back: the user
// gets that much more.
type askClock struct {
	mu     *sync.Mutex // p.mu, which the timer takes
	hidden func() bool // whether the question is out of sight; under mu
	why    error       // the cause the question ends with

	left  time.Duration // as of since
	since time.Time     // when the clock went on; zero while it stands
	timer *time.Timer   // of the time out, or of the next look
	gen   int           // the timer's: an older one finds itself stale
	out   chan struct{} // closed when the time is out
	done  bool          // out, or stopped
}

// askLook is how often the clock standing looks whether its question is in
// sight again. A variable: the tests shorten it.
var askLook = 50 * time.Millisecond

// answerClock starts the clock of the question opening on ctx: nil if the
// agent gave it no time to keep, which is a question waiting as long as
// ctx. Called under p.mu, the question on the screen or held.
func (p *Proxy) answerClock(ctx context.Context) *askClock {
	d, why, ok := agent.AnswerTime(ctx)
	if !ok {
		return nil
	}
	c := &askClock{mu: &p.mu, hidden: p.holding, why: why, left: d, out: make(chan struct{})}
	c.sync()
	return c
}

// sync stops the clock if the question is out of sight, and starts it
// again with the time left if it is back. Called under mu.
func (c *askClock) sync() {
	if c == nil || c.done {
		return
	}
	going := !c.since.IsZero()
	switch {
	case c.hidden():
		if going {
			c.left -= time.Since(c.since)
			c.since = time.Time{}
		}
		c.arm(askLook)
	case !going:
		c.since = time.Now()
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

// tick is the time out, or the clock standing looking again. Gone out of
// sight with nobody telling, the question stands with what was left: it
// does not end under the viewer.
func (c *askClock) tick(gen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done || gen != c.gen {
		return
	}
	if c.since.IsZero() || c.hidden() {
		c.sync()
		return
	}
	c.done = true
	close(c.out)
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
