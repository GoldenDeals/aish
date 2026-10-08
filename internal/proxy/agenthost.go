package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/tools"
)

// The agent lives in the proxy: one agent.Agent for the whole shell, with
// the journal, the compiled policies, the provider's connection and the
// MCP servers at hand between its calls. The `aish agent start|resume` and
// `aish compact` commands inside the shell only carry the request over RPC
// and hold the connection while the proxy works: closing it, or
// agent_cancel, stops the work.

// agentHost keeps the agent for the shell's requests, which run one at a
// time under reqMu (request); prepare.go readies the agent for each, and
// agentui.go is the journal, the shell and the terminal it is given. What
// the agent holds, the policies (which lock themselves) and untrusted are
// the request's, under reqMu; the rest is under p.mu.
type agentHost struct {
	reqMu sync.Mutex
	ag    *agent.Agent // made by the first request

	handed    string             // call id of the agent's command left for the shell, until its output is taken
	cancelReq context.CancelFunc // stops the request in progress
	reqCtx    context.Context    // and is its context
	cancelGen uint64             // agent_cancel calls so far
	overhead  int                // the agent's Overhead after the last request
	project   string             // the .aish.toml of the last request, "" if none

	policies     policy.Cache
	untrusted    map[string]bool // the project files tellUntrusted told of
	agentProv    llm.Provider    // the agent's provider, see providerFor
	agentProvKey string
}

// request runs fn on the agent for one RPC call, alone: the shell drives
// requests one at a time, and a request interrupted by Ctrl+C may still be
// closing when the next one arrives. A panic in the agent must not take
// the shell down.
func (p *Proxy) request(ctx context.Context, ex tools.Exec, fresh bool, fn func(context.Context, *agent.Agent) error) (err error) {
	p.mu.Lock()
	gen := p.cancelGen
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	if !p.takeTurn(gen, cancel) {
		cancel()
		return context.Canceled
	}
	defer p.reqMu.Unlock()
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("agent: %v", r)
			stack := bytes.ReplaceAll(debug.Stack(), []byte("\n"), []byte("\r\n"))
			p.mu.Lock()
			p.emit(fmt.Appendf(nil, "\r\n\x1b[31maish: %v\x1b[0m\r\n%s", r, stack))
			p.mu.Unlock()
		}
		p.mu.Lock()
		p.cancelReq, p.reqCtx = nil, nil
		p.mu.Unlock()
	}()
	p.mu.Lock()
	p.reqCtx = ctx
	// Checked again: the request that had the turn while this one waited
	// may have left a command for the shell.
	handed := fresh && p.handed != ""
	p.mu.Unlock()
	if handed {
		return errNested
	}
	a, err := p.prepare(ctx, ex, fresh)
	if err != nil {
		return err
	}
	err = fn(ctx, a)
	o := a.Overhead()
	p.mu.Lock()
	p.overhead = o
	p.mu.Unlock()
	if s := llm.Short(err); s != "" && !errors.Is(err, context.Canceled) {
		// The shell prints it: an SDK's error carries the URL, the request
		// ID and the raw body besides the API's type and message.
		err = errors.New(s)
	}
	return err
}

// takeTurn waits for the request before this one to end and makes this
// one the request in progress, the one agent_cancel stops with cancel. gen
// is how many times agent_cancel had been called when this request came:
// one since then was Ctrl+C on it, which reached only the request it was
// waiting for, so it gets no turn (false).
func (p *Proxy) takeTurn(gen uint64, cancel context.CancelFunc) bool {
	p.reqMu.Lock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancelGen != gen {
		p.reqMu.Unlock()
		return false
	}
	p.cancelReq = cancel
	return true
}

// errNested refuses `aish agent start` and `aish compact` run by the
// agent's own command: closing the call the shell is running as
// interrupted, they would leave the agent_resume after it no call to go on
// with. The shell's own `agent start` comes before the first command is
// handed off, and the next request of the same command line after the
// last command's output was taken.
var errNested = errors.New("the assistant's command cannot start or compact a request")

// errBusy refuses them while a request is in progress and not
// interrupted: the shell sends its next request only once the client of
// that one is gone, so one that comes meanwhile is from a process the
// request started or left running in the background — a subagent's
// command, a tool, a hook. Waiting for the turn, it would wait for the
// request that waits for that process, or close the call the request
// hands off next.
var errBusy = errors.New("a request is in progress: the assistant's commands cannot start or compact another")

// nested is why a request that starts or compacts is refused, nil if it is
// not. fg, what fromShell said of the caller, comes last: the reasons
// before it name the request in the way, and between requests it is the
// only one that a background subagent's command, or a job the agent's
// command left, meets. Called under p.mu.
func (h *agentHost) nested(fg error) error {
	switch {
	case h.handed != "":
		return errNested
	case h.reqCtx != nil && h.reqCtx.Err() == nil:
		return errBusy
	}
	return fg
}

// agentStart begins a request; it does what the ask-start marker does too,
// in case the request arrives first.
func (p *Proxy) agentStart(ctx context.Context, ap rpc.AgentParams) error {
	fg := p.fromShell(ctx)
	p.mu.Lock()
	if err := p.nested(fg); err != nil {
		p.mu.Unlock()
		return err
	}
	if !p.asking {
		p.asking, p.folds = true, nil
	}
	p.mu.Unlock()
	return p.request(ctx, execOf(ap), true, func(ctx context.Context, a *agent.Agent) error {
		// A request interrupted right after it left a command would have
		// the shell run that command after this one. Not before the turn:
		// the request that has it may hand a command off yet.
		_ = os.WriteFile(filepath.Join(p.run, "next.cmd"), nil, 0o600)
		return a.Start(ctx, ap.Text, p.shellExec(ap))
	})
}

// agentResume goes on after the command the shell was handed. The id of
// another call comes from a command, the agent's own or a subagent's: the
// agent would wait for an output that is not coming and go on without it.
// The agent's command resuming the call it runs as cannot be told from the
// shell, which sends the same.
func (p *Proxy) agentResume(ctx context.Context, ap rpc.AgentParams) error {
	p.mu.Lock()
	handed := p.handed
	p.mu.Unlock()
	if handed == "" || handed != ap.ID {
		return fmt.Errorf("the shell is not running the assistant's command %s", ap.ID)
	}
	if err := p.fromShell(ctx); err != nil {
		return err
	}
	return p.request(ctx, execOf(ap), false, func(ctx context.Context, a *agent.Agent) error {
		return a.Resume(ctx, ap.ID, ap.RC, p.shellExec(ap))
	})
}

func (p *Proxy) compact(ctx context.Context, ap rpc.AgentParams) error {
	fg := p.fromShell(ctx)
	p.mu.Lock()
	err := p.nested(fg)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return p.request(ctx, execOf(ap), true, func(ctx context.Context, a *agent.Agent) error {
		// The shell's name: the system prompt holds it, and the summary
		// is asked with the one the turns before it were cached with.
		ex := execOf(ap)
		ex.Shell = p.shell.Name()
		return a.Compact(ctx, ap.Text, ex)
	})
}

func execOf(ap rpc.AgentParams) tools.Exec { return tools.Exec{Dir: ap.Cwd, Env: ap.Env} }

// shellExec is execOf with the shell's name and the options it had on at
// its last prompt, as saveState read them: the policy reads the agent's
// commands as that shell does, in the modes it runs them in, set -k from
// ~/.bashrc included. A command of the request that turns one on is ahead
// of them until the next prompt; the policy marks such a line itself.
func (p *Proxy) shellExec(ap rpc.AgentParams) tools.Exec {
	ex := execOf(ap)
	ex.Shell = p.shell.Name()
	p.mu.Lock()
	ex.Opts = p.shellOpts()
	p.mu.Unlock()
	return ex
}

// cancelRequest stops the request in progress, if any, and those waiting
// for it: Ctrl+C reached the `aish agent` holding one of them, which asks
// for this and waits for the agent to finish before the shell goes on to
// its prompt.
func (p *Proxy) cancelRequest() {
	p.mu.Lock()
	p.cancelGen++
	cancel := p.cancelReq
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// stopBackground stops the agent's subagents in the background, if any.
// Not under p.mu: it waits for them, and their commands may call the proxy.
func (p *Proxy) stopBackground() {
	p.mu.Lock()
	a := p.ag
	p.mu.Unlock()
	if a != nil {
		a.StopBackground()
	}
}
