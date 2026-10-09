package proxy

import (
	"context"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// A session has the user's name, which `aish session rename` gives the
// session of this shell through rename, and the model's, asked for once
// the first request of the session was recorded (titleSession): what
// `aish resume --all` lists a session by that the user did not name.

// titles are the model's names asked for, under p.mu.
type titles struct {
	// on: Run turns it on. The tests of requests leave it off, their
	// providers answering only the calls they script.
	on bool
	// asked is the last session found to have a request: its first was
	// given a name, or was made before the shell came to it.
	asked string
	// stop cancels the calls in flight, which pending close as they end.
	ctx     context.Context
	stop    context.CancelFunc
	pending []chan struct{}
}

// titleTimeout bounds a call for a name: it is not worth more.
const titleTimeout = 30 * time.Second

// titleWait is how long the exit of the shell waits for the names still
// asked for: the user may leave right after the first answer.
const titleWait = 3 * time.Second

// rename gives this shell's session the user's name, "" none: the proxy
// holds the session, its lock, and the name of one not on disk yet.
func (p *Proxy) rename(name string) (rpc.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.sess.SetName(name); err != nil {
		return rpc.Info{}, err
	}
	return p.info(), nil
}

// untitled is the shell's session if the request about to start may be its
// first, nil if not: the session has a request already.
func (p *Proxy) untitled() *session.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.titles.on || p.titles.asked == p.sess.ID {
		return nil
	}
	if hasRequest(p.sess.Entries()) {
		p.titles.asked = p.sess.ID
		return nil
	}
	return p.sess
}

// titleSession asks the model, in the background, for a name of sess,
// which untitled found without a request before a.Start took text: if
// the request was recorded, the hooks letting it through, it was the
// first. Neither the request nor the prompt waits for the name, a failed
// call goes untold, and none is asked for again.
func (p *Proxy) titleSession(sess *session.Session, a *agent.Agent, text string) {
	if sess == nil || !hasRequest(sess.Entries()) {
		return
	}
	// The request's: prepare writes them under reqMu, which is held.
	prov, cfg := a.Provider, a.Cfg
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.titles.on || p.titles.asked == sess.ID {
		return
	}
	p.titles.asked = sess.ID
	if p.titles.ctx == nil {
		p.titles.ctx, p.titles.stop = context.WithCancel(context.Background())
	}
	ctx, cancel := context.WithTimeout(p.titles.ctx, titleTimeout)
	done := make(chan struct{})
	p.titles.pending = append(p.titles.pending, done)
	go func() {
		defer close(done)
		defer cancel()
		defer func() { _ = recover() }() // a name is not worth the shell
		if title, err := agent.Title(ctx, prov, cfg, text); err == nil && ctx.Err() == nil {
			_ = sess.SetTitle(title)
		}
	}()
}

// waitTitles gives the names asked for up to wait to come, then cancels
// them, and asks for no more: the shell is gone, and the lock of its
// session goes next.
func (p *Proxy) waitTitles(wait time.Duration) {
	p.mu.Lock()
	p.titles.on = false
	pending, stop := p.titles.pending, p.titles.stop
	p.mu.Unlock()
	deadline := time.After(wait)
	for _, done := range pending {
		select {
		case <-done:
			continue
		case <-deadline:
			stop()
		}
		<-done
	}
}

func hasRequest(es []session.Entry) bool {
	for _, e := range es {
		if e.Kind == session.KindUser {
			return true
		}
	}
	return false
}
