// Package proxy runs the user's bash inside a pseudo-terminal, passes all
// bytes through untouched except aish markers, and records command output
// into the session.
package proxy

import (
	"sync"

	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// Proxy owns the session, the shell's terminal and the agent. Its state is
// split by subsystem, each a struct embedded here and kept by the code of
// its own files; they share p.mu, which guards all of it but what the doc
// of a subsystem says otherwise. Proxy puts them together: New, Run
// (run.go) and the RPC methods (handle.go).
type Proxy struct {
	mu   sync.Mutex
	sess *session.Session // the journal; resume switches it, see session
	mcp  *mcp.Manager     // Run makes it before the socket opens; Reload keeps it

	console    // the terminal: what goes to it, who reads the keys (console.go)
	recorder   // the shell's output by command, and the folds (recorder.go, markers.go)
	shellState // the shell's process and its state (shellstate.go)
	modelState // the shell's profile, model, effort and window (modelstate.go)
	agentHost  // the agent and its requests (agenthost.go)
	settings   // the config in force (applyconfig.go)
}

func New(sess *session.Session) *Proxy {
	return &Proxy{
		sess: sess,
		mcp:  mcp.NewManager(nil, ""),
		recorder: recorder{
			agent:   map[string]*segment{},
			done:    map[string]rpc.Output{},
			waiters: map[string]chan struct{}{},
		},
	}
}

// session is the current session for code that does not hold p.mu.
func (p *Proxy) session() *session.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess
}
