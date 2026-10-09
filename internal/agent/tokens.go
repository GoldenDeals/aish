package agent

import (
	"encoding/json"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
)

// contextSize is session.Tokens as the agent counts its context: by its
// max_output_bytes, with the system prompt and the tool schemas of its
// next request as the overhead.
func (a *Agent) contextSize(es []session.Entry) session.Estimate {
	return session.Tokens(es, a.Cfg.MaxOutputBytes, overhead(a.request(nil)))
}

// contextTokens is the size of the context es, in tokens (contextSize).
func (a *Agent) contextTokens(es []session.Entry) int { return a.contextSize(es).Tokens }

// Overhead is the system prompt and the tool schemas in bytes, what
// session.Tokens adds to the journal for the host. The host calls it after
// a request, while that request's environment is still the agent's: the
// next one may be made in another directory.
func (a *Agent) Overhead() int { return overhead(a.request(nil)) }

// overhead is what a request carries besides its messages, in bytes.
func overhead(req llm.Request) int {
	n := len(req.System)
	if len(req.Tools) > 0 {
		b, _ := json.Marshal(req.Tools)
		n += len(b)
	}
	return n
}

// CompactLimit is the context size, in tokens, past which the session is
// compacted before the agent's next turn: compact_at of the window, 0 when
// either is unknown or off. The status at the prompt marks it by the same
// config, the window of the shell's model filled in as the agent has it.
func CompactLimit(cfg config.Config) int {
	return int(cfg.CompactAt * float64(cfg.ContextWindow))
}
