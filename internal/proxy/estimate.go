package proxy

import "github.com/inebotov/aish/internal/session"

// contextTokens is session.Tokens as the agent counts it: till a turn
// after a summary is measured, the system prompt and the tool schemas of
// the last request are added, which session.Tokens cannot see and the next
// request carries all the same. measured is whether such a turn is there.
// Called under p.mu.
func (p *Proxy) contextTokens(es []session.Entry) (n int, measured bool) {
	n = session.Tokens(es, p.maxOutput)
	for _, e := range session.Current(es) {
		if e.InputTokens > 0 {
			return n, true
		}
	}
	// An empty context stays empty: there is nothing to show yet.
	if n > 0 {
		n += p.overhead
	}
	return n, false
}
