package agent

import (
	"encoding/json"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
)

// contextTokens is session.Tokens with what it cannot see before the first
// turn after a summary is measured: the system prompt and the tool schemas,
// which the API counts into InputTokens.
func (a *Agent) contextTokens(es []session.Entry) int {
	n := session.Tokens(es, a.Cfg.MaxOutputBytes)
	for _, e := range session.Current(es) {
		if e.InputTokens > 0 {
			return n
		}
	}
	return n + overhead(a.request(nil))
}

// overhead is what a request carries besides its messages, at the four
// bytes a token session.Tokens counts.
func overhead(req llm.Request) int {
	n := len(req.System)
	if len(req.Tools) > 0 {
		b, _ := json.Marshal(req.Tools)
		n += len(b)
	}
	return n / 4
}
