package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/mcp"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/skills"
	"github.com/inebotov/aish/internal/tools"
)

// The agent lives in the proxy: one agent.Agent for the whole shell, with
// the journal, the compiled policies, the provider's connection and the
// MCP servers at hand between its calls. The `aish agent start|resume` and
// `aish compact` commands inside the shell only carry the request over RPC
// and hold the connection while the proxy works: closing it, or
// agent_cancel, stops the work.

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
		p.cancelReq = nil
		p.mu.Unlock()
	}()
	a, err := p.prepare(ctx, ex, fresh)
	if err != nil {
		return err
	}
	return fn(ctx, a)
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

// agentStart begins a request; it does what the ask-start marker does too,
// in case the request arrives first.
func (p *Proxy) agentStart(ctx context.Context, ap rpc.AgentParams) error {
	p.mu.Lock()
	if !p.asking {
		p.asking, p.folds = true, nil
	}
	p.mu.Unlock()
	// A request interrupted right after it left a command would have the
	// shell run that command after this one.
	_ = os.WriteFile(filepath.Join(p.run, "next.cmd"), nil, 0o600)
	return p.request(ctx, execOf(ap), true, func(ctx context.Context, a *agent.Agent) error {
		return a.Start(ctx, ap.Text, execOf(ap))
	})
}

func (p *Proxy) agentResume(ctx context.Context, ap rpc.AgentParams) error {
	return p.request(ctx, execOf(ap), false, func(ctx context.Context, a *agent.Agent) error {
		return a.Resume(ctx, ap.ID, ap.RC, execOf(ap))
	})
}

func (p *Proxy) compact(ctx context.Context, ap rpc.AgentParams) error {
	return p.request(ctx, execOf(ap), true, func(ctx context.Context, a *agent.Agent) error {
		return a.Compact(ctx, ap.Text, execOf(ap))
	})
}

func execOf(ap rpc.AgentParams) tools.Exec { return tools.Exec{Dir: ap.Cwd, Env: ap.Env} }

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

// prepare gives the agent what this request needs: the config as it is
// now, with this shell's model and effort; the provider, kept while they
// stay the same; the policies, compiled again only when their files or
// the [policy] rules change; and, for a fresh request, the tools of the
// shell's directory.
func (p *Proxy) prepare(ctx context.Context, ex tools.Exec, fresh bool) (*agent.Agent, error) {
	// What config.toml selects now: what the status compares the shell's
	// profile with, and where a shell goes whose profile is gone. Load
	// fails where the shell's profile may not, on a profile $AISH_PROFILE
	// or the profile key names and config.toml has not: LoadProfile tells
	// what is wrong if the shell's profile is too.
	def, defErr := config.Load()
	p.mu.Lock()
	if defErr == nil {
		p.defProfile = def.Profile
		if _, ok := def.Profiles[p.profile]; p.profile != "" && !ok {
			if err := p.leaveGone(def); err != nil {
				p.mu.Unlock()
				return nil, err
			}
		}
	}
	// One moment for all of them: `aish model` may land in between. The
	// shell's profile, not the one config.toml selects, which `aish model`
	// may have switched from.
	profile, model, effort, window, fixed := p.profile, p.model, p.effort, p.window, p.fixedWindow
	if window > 0 {
		p.windowAsked = "" // see lookupOnce
	}
	p.mu.Unlock()
	cfg, err := config.LoadProfile(profile)
	if err != nil {
		return nil, err
	}
	cfg, project, err := config.Project(cfg, ex.Dir)
	if err != nil {
		return nil, err
	}
	if fresh {
		p.tellUntrusted(project, cfg.Untrusted)
	}
	p.mu.Lock()
	p.project = project
	cfg.Model, cfg.Effort = model, effort
	if cfg.ContextWindow == 0 {
		cfg.ContextWindow = window // the API's or `aish model`'s, for compact_at
	}
	a := p.ag
	if a == nil {
		a = &agent.Agent{Journal: journal{p}, Shell: shell{p}, UI: &ui{p: p}}
		p.ag = a
	}
	p.mu.Unlock()
	// The key as the shell has it: the user may have exported it there.
	cfg.APIKey = llm.Key(cfg, ex.Getenv)
	prov, err := p.providerFor(cfg)
	if err != nil {
		return nil, err
	}
	// Not waited for: the first request must not stand on the models list.
	// The window reaches cfg with the next prepare.
	if window == 0 && !fixed {
		p.mu.Lock()
		p.lookupOnce(prov, profile, model, cfg.APIKey)
		p.mu.Unlock()
	}
	rules := policy.Rules{Deny: cfg.Policy.Deny, Ask: cfg.Policy.Ask, WriteOutsideHome: cfg.Policy.WriteOutsideHome}
	pol, err := p.policies.Engine(ctx, cfg.PolicyDir, rules)
	if err != nil {
		return nil, err
	}
	a.Cfg, a.Provider, a.Policy = cfg, prov, pol
	if fresh || a.Tools == nil {
		a.Tools = p.loadTools(ctx, cfg, ex.Dir)
	}
	return a, nil
}

// providerFor is the provider for cfg, the last one while nothing it
// depends on changed, so that its connections are kept between turns.
func (p *Proxy) providerFor(cfg config.Config) (llm.Provider, error) {
	key := fmt.Sprint(cfg.Provider, "\x00", cfg.BaseURL, "\x00", cfg.APIKey, "\x00", cfg.Model, "\x00", cfg.Effort, "\x00", cfg.MaxTokens)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.agentProv != nil && p.agentProvKey == key {
		return p.agentProv, nil
	}
	prov, err := p.makeProvider(cfg)
	if err != nil {
		return nil, err
	}
	p.agentProv, p.agentProvKey = prov, key
	return prov, nil
}

// loadTools returns the built-in and external tools, the skills of cwd
// the model may invoke, and the MCP tools known so far: waiting for a
// server still warming up would stall the request with no output. Such a
// server reaches the model with the next request.
func (p *Proxy) loadTools(ctx context.Context, cfg config.Config, cwd string) *tools.Registry {
	reg := tools.Load(cfg.ToolsDir)
	found, _ := skills.Find(cwd)
	for _, s := range found {
		if !s.UserOnly {
			reg.Add(s.Tool())
		}
	}
	local, _ := mcp.Local(ctx, p.mcp, false)
	for _, t := range local {
		reg.Add(t)
	}
	return reg
}

// journal is the proxy's session for the agent: the session object changes
// with `aish resume`, so it is looked up at each call.
type journal struct{ p *Proxy }

func (j journal) ID() string {
	j.p.mu.Lock()
	defer j.p.mu.Unlock()
	return j.p.sess.ID
}
func (j journal) Len() int                         { return j.p.session().Len() }
func (j journal) Entries() []session.Entry         { return j.p.session().Entries() }
func (j journal) Append(es ...session.Entry) error { return j.p.session().Append(es...) }

// shell is the user's bash for the agent: a command is left in $AISH_RUN
// for __aish_ask to run, and its output comes back with the agent-end
// marker.
type shell struct{ p *Proxy }

func (s shell) HandOff(id, cmd string) error {
	if err := os.WriteFile(filepath.Join(s.p.run, "next.id"), []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.p.run, "next.cmd"), []byte(cmd), 0o600)
}

func (s shell) Wait(ctx context.Context, id string, timeout time.Duration) (rpc.Output, error) {
	return s.p.wait(ctx, id, timeout)
}

// ui is the terminal for the agent. Everything goes through emit under
// p.mu, like the shell's output, so Ctrl+O cannot interleave with it. The
// terminal is raw: a bare newline would not return the carriage, which the
// PTY used to do for the agent.
type ui struct {
	p  *Proxy
	cr crlf
}

func (u *ui) Write(b []byte) (int, error) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	u.p.emit(u.cr.fix(b))
	return len(b), nil
}

func (u *ui) Size() (int, int) {
	if u.p.size == nil {
		return 0, 0
	}
	return u.p.size()
}

func (u *ui) Ask(ctx context.Context, q string) (string, error) { return u.p.askUser(ctx, q) }

func (u *ui) Form(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	return u.p.askForm(ctx, qs)
}

// Fold shows the status of a result as a command's is shown: drawn by the
// same code, on the line of the call if it was left open. The text is
// kept as it came, whole.
func (u *ui) Fold(title, text string) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	f := newResult(title, text)
	f.at, u.p.at = u.p.at, nil
	u.p.emit(f.finish(-1))
	u.p.folds = append(u.p.folds, Fold{Title: title, Text: text})
}

func (u *ui) Live(title string) agent.Live {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	l := &live{p: u.p}
	at := u.p.at
	u.p.at = nil
	if u.p.foldLines >= 0 {
		l.f = newFold(title, u.p.foldLines)
		u.p.tool = l.f
	}
	if l.f != nil && u.p.foldLines == 0 {
		l.f.at = at
	} else if at != nil {
		u.p.emit([]byte("\r\n")) // the output shows below the call
	}
	return l
}

// CommandAt needs no long: the status goes at the right edge of the last
// line, however many the call took.
func (u *ui) CommandAt(col int, _ bool, hidden int) {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	if u.p.size == nil {
		return
	}
	if w, _ := u.p.size(); col >= 0 && w > 0 {
		u.p.at = &statusAt{col: min(col, w), cols: w, hidden: hidden}
	}
}

// live is an external tool's output as it runs, folded like a command's;
// without folding (fold_lines < 0) it passes through.
type live struct {
	p  *Proxy
	f  *fold
	cr crlf
}

func (l *live) Write(b []byte) (int, error) {
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	show := l.cr.fix(b)
	if l.f != nil {
		show = l.f.write(show)
	}
	l.p.emit(show)
	return len(b), nil
}

func (l *live) Finish(exit int) {
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	if l.f != nil && l.p.tool == l.f {
		l.p.finishFold(l.f, exit)
		l.p.tool = nil
	}
}

// crlf turns each newline into a carriage return and newline, across
// writes, unless it has one already.
type crlf struct{ cr bool }

func (c *crlf) fix(b []byte) []byte {
	if !bytes.Contains(b, []byte{'\n'}) {
		if len(b) > 0 {
			c.cr = b[len(b)-1] == '\r'
		}
		return b
	}
	out := make([]byte, 0, len(b)+bytes.Count(b, []byte{'\n'}))
	for _, ch := range b {
		if ch == '\n' && !c.cr {
			out = append(out, '\r')
		}
		out = append(out, ch)
		c.cr = ch == '\r'
	}
	return out
}

// prompt is a question the agent has open on the terminal; the proxy reads
// the answer from the keyboard, as it does the viewer's keys.
type prompt struct {
	line []byte
	done chan string
}

// askUser prints q and waits for a line, or for ctx: Ctrl+C goes to the
// shell, which stops the request.
func (p *Proxy) askUser(ctx context.Context, q string) (string, error) {
	p.mu.Lock()
	if p.ask != nil || p.form != nil {
		p.mu.Unlock()
		return "", errors.New("a question is open already")
	}
	pr := &prompt{done: make(chan string, 1)}
	p.ask = pr
	p.emit([]byte(q))
	p.mu.Unlock()
	select {
	case ans := <-pr.done:
		return ans, nil
	case <-ctx.Done():
		p.mu.Lock()
		if p.ask == pr {
			p.ask = nil
		}
		p.mu.Unlock()
		return "", ctx.Err()
	}
}

// askKey reads the answer to the open question from what the user typed
// and echoes it: the shell is not reading, so nothing else would. Returns
// what goes on to the shell anyway. Called under p.mu.
func (p *Proxy) askKey(b []byte) []byte {
	var pass []byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '\r' || c == '\n':
			p.emit([]byte("\r\n"))
			p.ask.done <- string(p.ask.line)
			p.ask = nil
			return append(pass, b[i+1:]...)
		case c == 0x7f || c == 0x08:
			if n := len(p.ask.line); n > 0 {
				_, size := utf8.DecodeLastRune(p.ask.line)
				p.ask.line = p.ask.line[:n-size]
				p.emit([]byte("\b \b"))
			}
		case c == 0x03:
			pass = append(pass, c) // interrupts the request, like anywhere else
		case c == ctrlO:
			if folds := p.viewFolds(); len(folds) > 0 && p.size != nil {
				w, h := p.size()
				p.view = newViewer(folds, w, h)
				_, _ = p.out.Write(p.view.open())
				return pass // the rest would be the viewer's
			}
		case c == 0x1b:
			// An escape sequence, an arrow key say: skip it.
			i++
			if i < len(b) && b[i] == '[' {
				for i++; i < len(b) && (b[i] < 0x40 || b[i] > 0x7e); i++ {
				}
			} else if i < len(b) && b[i] == 'O' {
				i++
			}
		case c >= 0x20:
			p.ask.line = append(p.ask.line, c)
			p.emit([]byte{c})
		}
	}
	return pass
}

// openForm is the form of ask_user while it is on the terminal; shown are
// the lines of its frame there.
type openForm struct {
	f     *form
	shown []string
	done  chan struct{}
}

// askForm shows the questions and waits for the user to answer or cancel
// them, or for ctx: Ctrl+C goes to the shell, which stops the request. The
// form is drawn in place below the call: the alternate screen would hide
// what the questions are about. The cursor is hidden meanwhile; the form
// draws its own where the user types.
func (p *Proxy) askForm(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	p.mu.Lock()
	if p.size == nil {
		p.mu.Unlock()
		return nil, errors.New("no terminal")
	}
	if p.ask != nil || p.form != nil {
		p.mu.Unlock()
		return nil, errors.New("a question is open already")
	}
	of := &openForm{f: newForm(qs), done: make(chan struct{})}
	p.form = of
	p.at = nil // the agent closed the line of the call: no status goes there
	p.emit([]byte("\x1b[?25l"))
	p.drawForm()
	p.mu.Unlock()
	select {
	case <-of.done:
		return of.f.answers(), nil
	case <-ctx.Done():
		p.mu.Lock()
		if p.form == of {
			p.closeForm()
		}
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

// formKey gives what the user typed to the open form and draws it anew.
// Ctrl+C goes on to the shell and Ctrl+O opens the viewer, as with a
// question. Returns what goes on to the shell. Called under p.mu.
func (p *Proxy) formKey(b []byte) []byte {
	view := false
	if i := bytes.IndexByte(b, ctrlO); i >= 0 && len(p.viewFolds()) > 0 {
		b, view = b[:i], true // the rest would be the viewer's
	}
	var keys, pass []byte
	for _, c := range b {
		if c == 0x03 {
			pass = append(pass, c) // interrupts the request, like anywhere else
		} else {
			keys = append(keys, c)
		}
	}
	if len(keys) > 0 {
		if p.form.f.feed(keys) {
			p.closeForm()
			return pass
		}
		p.drawForm()
	}
	if view {
		w, h := p.size()
		p.view = newViewer(p.viewFolds(), w, h)
		_, _ = p.out.Write(p.view.open())
	}
	return pass
}

// drawForm draws the open form over its last frame. Called under p.mu.
func (p *Proxy) drawForm() {
	of := p.form
	w, _ := p.size()
	frame := of.f.frame(w)
	p.emit([]byte(of.erase(w) + frame))
	of.shown = strings.Split(frame, "\r\n")
}

// closeForm takes the form off the screen and leaves the summary of the
// answers in its place, if there are any. Called under p.mu.
func (p *Proxy) closeForm() {
	of := p.form
	p.form = nil
	w, _ := p.size()
	out := of.erase(w)
	if s := of.f.summary(); s != "" {
		out += s + "\r\n"
	}
	p.emit([]byte(out + "\x1b[?25h"))
	close(of.done)
}

// erase goes back to the first line of the frame on the screen and clears
// the screen from there. The lines are counted at the width the terminal
// has now: narrowed, it may have wrapped them.
func (of *openForm) erase(cols int) string {
	if of.shown == nil {
		return ""
	}
	cols = max(cols, 1)
	rows := 0
	for _, l := range of.shown {
		rows += max(1, (frameWidth(l)+cols-1)/cols)
	}
	s := "\r"
	if rows > 1 {
		s += fmt.Sprintf("\x1b[%dA", rows-1)
	}
	return s + "\x1b[J"
}

// status counts what `aish status` shows. Called under p.mu.
func (p *Proxy) status() rpc.Status {
	all := p.sess.Entries()
	es := session.Current(all)
	st := rpc.Status{Info: p.info(), ProjectConfig: p.project, Tokens: session.Tokens(es, p.maxOutput)}
	for _, e := range all {
		switch e.Kind {
		case session.KindSummary:
			st.Compacts++
		case session.KindAssistant:
			st.ToolCalls += len(e.ToolCalls)
			st.InputTokens += e.InputTokens
			st.CachedTokens += e.CachedTokens
			st.OutputTokens += e.OutputTokens
		}
	}
	for _, e := range es {
		switch e.Kind {
		case session.KindShell:
			st.Commands++
		case session.KindUser:
			st.Requests++
		case session.KindAssistant:
			st.Measured = st.Measured || e.InputTokens > 0
		}
	}
	return st
}
