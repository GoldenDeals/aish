// Package proxy runs the user's bash inside a pseudo-terminal, passes all
// bytes through untouched except aish markers, and records command output
// into the session.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/bashstate"
	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/mcp"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/shellinit"
	"github.com/inebotov/aish/internal/tools"
)

const (
	headCap = 64 << 10
	tailCap = 64 << 10
)

type segment struct {
	cmd     string
	buf     *capture.Buffer
	fold    *fold // agent commands only
	cleared bool  // the command erased the screen
}

// Fold is one output hidden behind "ctrl+o to expand".
type Fold = rpc.Fold

// Proxy owns the session, the output recorder and the agent.
type Proxy struct {
	sess         *session.Session
	foldLines    int
	maxOutput    int
	promptStatus bool
	compactAt    float64      // compact_at: the status says when the next request compacts
	ignore       []string     // journal_ignore: commands recorded without their output
	stateIgnore  []string     // state_ignore: variables kept out of the shell state
	fixedWindow  bool         // context_window is set in the config
	prov         llm.Provider // for the models list and the levels of effort; nil if unknown
	out          io.Writer    // the terminal
	size         func() (w, h int)

	// Commands are aish subcommands the shell gets as commands of their
	// own (`status` for `aish status`); set before Run.
	Commands []string

	mu      sync.Mutex
	screen  Screen
	asking  bool                // inside __aish_ask, between ask-start and the next prompt
	user    *segment            // command typed by the user, between cmd-start and cmd-end
	agent   map[string]*segment // commands run on behalf of the agent, by call id
	tool    *fold               // live output of an external tool, while it runs
	at      *statusAt           // where the agent left the cursor after printing its next command
	folds   []Fold              // folded outputs of the last request, for Ctrl+O
	view    *viewer             // open while Ctrl+O shows the folds
	held    []byte              // shell output that arrived while the viewer was open
	ask     *prompt             // a question the agent waits for the user to answer
	form    *openForm           // the questions of ask_user while the user answers them
	done    map[string]rpc.Output
	waiters map[string]chan struct{}
	mcp     *mcp.Manager
	model   string // `aish model` switches it for this shell
	effort  string // and this, "" being the model's default
	window  int    // its context size, 0 if unknown
	profile string // and the profile of config.toml they are of, "" for its top level
	// defProfile is the one config.toml selected, which the status does
	// not name.
	defProfile string

	// The shell's state: how it started, how it was at the last prompt, and
	// what of it was saved last (session id and all).
	run       string
	base, cur *bashstate.State
	lastSaved []byte
	switched  bool // `aish resume` switched the session during this command

	restore string // the script that brings back a resumed session
	resumed *session.Saved

	// The agent, see agent.go. cancelReq is under p.mu; the rest is the
	// request's own, one at a time under reqMu.
	reqMu        sync.Mutex
	ag           *agent.Agent
	cancelReq    context.CancelFunc
	policies     policy.Cache
	project      string // the .aish.toml of the last request, "" if none
	untrusted    map[string]bool
	agentProv    llm.Provider
	agentProvKey string
	newProvider  func(config.Config) (llm.Provider, error) // nil: llm.New; tests set it
}

func New(sess *session.Session) *Proxy {
	return &Proxy{
		sess:    sess,
		agent:   map[string]*segment{},
		done:    map[string]rpc.Output{},
		waiters: map[string]chan struct{}{},
		mcp:     mcp.NewManager(nil, ""),
	}
}

// Run starts bash and blocks until it exits, returning its exit code.
func (p *Proxy) Run(cfg config.Config, reg *tools.Registry) (int, error) {
	p.foldLines = cfg.FoldLines
	p.maxOutput = cfg.MaxOutputBytes
	p.promptStatus = cfg.PromptStatus
	p.compactAt = cfg.CompactAt
	p.ignore, p.stateIgnore = cfg.JournalIgnore, cfg.StateIgnore
	p.fixedWindow = cfg.ContextWindow > 0
	if prov, err := p.listProvider(cfg); err == nil {
		p.prov = prov
	}
	// setModel may start a lookup of the window, which takes p.mu.
	p.mu.Lock()
	p.profile, p.defProfile = cfg.Profile, cfg.Profile
	p.effort, p.window = cfg.Effort, cfg.ContextWindow
	p.setModel(cfg.Model, cfg.ContextWindow)
	if p.resumed != nil {
		p.restoreModel(*p.resumed)
	}
	p.mu.Unlock()
	p.out = os.Stdout
	p.size = func() (int, int) {
		w, h, err := term.GetSize(int(os.Stdin.Fd()))
		if err != nil || w == 0 || h == 0 {
			return 80, 24
		}
		return w, h
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return 1, errors.New("aish must be started from a terminal")
	}
	self, err := os.Executable()
	if err != nil {
		return 1, err
	}
	if err := p.sess.Lock(); err != nil {
		return 1, err
	}
	defer func() { p.session().Unlock() }()
	// After Lock: the session resumed is as old as it was left.
	if ttl := cfg.SessionsMaxAge(); ttl > 0 {
		pruned, err := session.Prune(cfg.SessionsDir, ttl)
		if len(pruned) > 0 {
			fmt.Fprintf(os.Stderr, "\x1b[2maish: pruned the sessions not used for %s: %d\x1b[0m\n", cfg.SessionsTTL, len(pruned))
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "aish: pruning sessions: %v\n", err)
		}
	}
	nonce := rand.Text()
	run, err := makeRunDir(reg, p.Commands, self, nonce, cfg.Route)
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(run)
	p.run = run
	if p.restore != "" {
		if err := os.WriteFile(filepath.Join(run, "restore.bash"), []byte(p.restore), 0o600); err != nil {
			return 1, err
		}
	}

	servers, err := mcp.LoadConfig(cfg.MCPConfig)
	if err != nil {
		// A broken MCP config must not keep the shell from starting.
		fmt.Fprintf(os.Stderr, "aish: %v\n", err)
	}
	p.mcp = mcp.NewManager(servers, filepath.Join(config.CacheDir(), "mcp"))
	p.mcp.Bin, p.mcp.Self = filepath.Join(run, "bin"), self
	p.mcp.Taken = func(name string) bool {
		_, ok := reg.Get(name)
		return ok || slices.Contains(p.Commands, name)
	}
	p.mcp.Warm()
	defer p.mcp.Close()

	sock := filepath.Join(run, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return 1, err
	}
	defer l.Close()
	go rpc.Serve(l, p.handle)

	bash, err := bashPath(cfg.Shell)
	if err != nil {
		return 1, err
	}
	cmd := exec.Command(bash, "--rcfile", filepath.Join(run, "rc"), "-i")
	cmd.Env = append(os.Environ(),
		"AISH_SOCK="+sock,
		"AISH_RUN="+run,
		"AISH_BIN="+self,
		"AISH_SESSION="+p.sess.ID,
		"AISH_TOOLS_PATH="+filepath.Join(run, "bin")+":"+cfg.ToolsDir,
	)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 1, err
	}
	defer ptmx.Close()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			_ = pty.InheritSize(os.Stdin, ptmx)
			p.resized()
		}
	}()
	winch <- syscall.SIGWINCH
	defer signal.Stop(winch)

	hup := make(chan os.Signal, 2)
	signal.Notify(hup, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(hup)
	stopSignals := forwardSignals(hup, cmd.Process, shutdownGrace)

	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return 1, err
	}
	defer term.Restore(int(os.Stdin.Fd()), old)

	go p.input(os.Stdin, ptmx)

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		p.pump(ptmx, NewFilter(nonce))
	}()

	waitErr := cmd.Wait()
	stopSignals()
	// Drain whatever bash wrote last; the PTY reports EIO once it is gone.
	select {
	case <-outDone:
	case <-time.After(200 * time.Millisecond):
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, waitErr
}

// makeRunDir creates the session's directory with the command wrappers in
// bin: one per name in cmds (`exec self NAME`) and one per Wrappable tool
// (`exec self tool NAME`). A name that is a command already gets no
// wrapper: bin comes first in PATH and would hide it for the whole shell.
// The subcommands go first, so a tool with such a name is left to `aish
// tool`.
func makeRunDir(reg *tools.Registry, cmds []string, self, nonce string, route config.Route) (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	run, err := os.MkdirTemp(base, "aish-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(run, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		return "", err
	}
	for _, c := range cmds {
		if _, err := exec.LookPath(c); err == nil {
			continue // `expand` is coreutils; `aish expand` and Ctrl+O remain
		}
		script := fmt.Sprintf("#!/bin/sh\nexec %q %s \"$@\"\n", self, c)
		if err := os.WriteFile(filepath.Join(bin, c), []byte(script), 0o755); err != nil {
			return "", err
		}
	}
	for _, t := range reg.All() {
		if !tools.Wraps(t) {
			continue
		}
		name := t.Name()
		if _, err := exec.LookPath(name); err == nil {
			continue // the wrapper would shadow it for the whole shell
		}
		if slices.Contains(cmds, name) {
			continue
		}
		script := fmt.Sprintf("#!/bin/sh\nexec %q tool %s \"$@\"\n", self, name)
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			return "", err
		}
	}
	for _, f := range []string{"next.cmd", "next.id"} {
		if err := os.WriteFile(filepath.Join(run, f), nil, 0o600); err != nil {
			return "", err
		}
	}
	// The shell and the agent read the nonce from here: in the environment
	// every command would inherit it.
	if err := os.WriteFile(filepath.Join(run, "nonce"), []byte(nonce+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(run, "route"), routeFile(route), 0o600); err != nil {
		return "", err
	}
	return run, os.WriteFile(filepath.Join(run, "rc"), []byte(shellinit.RCFile()), 0o600)
}

// bashPath is the bash to run: the configured one, else the user's login
// shell if it is a bash, as it need not be the first one in PATH (a newer
// bash in /opt, an old /bin/bash on macOS).
func bashPath(configured string) (string, error) {
	if configured != "" {
		p, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("shell in config: %w", err)
		}
		return p, nil
	}
	if sh := os.Getenv("SHELL"); filepath.Base(sh) == "bash" {
		if p, err := exec.LookPath(sh); err == nil {
			return p, nil
		}
	}
	return exec.LookPath("bash")
}

// input copies the keyboard to bash. Ctrl+O while the assistant works or
// at the prompt toggles the viewer of folded outputs instead.
func (p *Proxy) input(r io.Reader, w io.Writer) {
	buf := make([]byte, 4<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if b := p.key(buf[:n]); len(b) > 0 {
				if _, err := w.Write(b); err != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

const ctrlO = 0x0f

// key handles the viewer and returns the input meant for bash.
func (p *Proxy) key(b []byte) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.view != nil {
		if p.view.key(b) {
			p.closeView()
		} else {
			_, _ = p.out.Write(p.view.render())
		}
		return nil
	}
	if p.ask != nil {
		return p.askKey(b)
	}
	if p.form != nil {
		return p.formKey(b)
	}
	i := bytes.IndexByte(b, ctrlO)
	// A user's command (an editor, say) gets Ctrl+O as usual.
	if i < 0 || p.user != nil {
		return b
	}
	folds := p.viewFolds()
	if len(folds) == 0 {
		if p.asking {
			return append(b[:i:i], b[i+1:]...) // would only be echoed as ^O
		}
		return b // readline's own Ctrl+O
	}
	w, h := p.size()
	p.view = newViewer(folds, w, h)
	_, _ = p.out.Write(p.view.open())
	return b[:i]
}

// viewFolds are the outputs of the current or last request, including the
// one being printed.
func (p *Proxy) viewFolds() []Fold {
	folds := append([]Fold{}, p.folds...)
	if f := p.liveFold(); f != nil && !f.open {
		// A command cut short on the screen is there before it prints anything.
		if raw := f.raw.Bytes(); len(raw) > 0 || f.cut() {
			folds = append(folds, Fold{Title: f.title + "  (running)", Text: string(raw)})
		}
	}
	return folds
}

func (p *Proxy) closeView() {
	_, _ = p.out.Write(p.view.close())
	_, _ = p.out.Write(p.held)
	p.view, p.held = nil, nil
}

func (p *Proxy) resized() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.view != nil {
		p.view.resize(p.size())
		_, _ = p.out.Write(append([]byte("\x1b[2J"), p.view.render()...))
	}
	if p.form != nil {
		p.drawForm() // held while the viewer is open
	}
}

// emit writes to the terminal, or holds the output while the viewer is open.
func (p *Proxy) emit(b []byte) {
	if p.view != nil {
		p.held = append(p.held, b...)
		return
	}
	_, _ = p.out.Write(b)
}

// liveFold is the fold of the output being printed right now, if any.
func (p *Proxy) liveFold() *fold {
	if p.tool != nil {
		return p.tool
	}
	for _, s := range p.agent {
		if s.fold != nil {
			return s.fold
		}
	}
	return nil
}

// pump copies PTY output to the terminal, stripping markers and feeding
// the recorder.
func (p *Proxy) pump(r io.Reader, f *Filter) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			f.Feed(buf[:n], p.output, p.marker)
		}
		if err != nil {
			return
		}
	}
}

// output records b and shows it, folded if it belongs to a long agent output.
// The terminal is written under the lock so Ctrl+O cannot interleave.
func (p *Proxy) output(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	show := b
	if cleared, from := p.screen.Feed(b); cleared && !p.asking {
		p.cleared()
		if p.user != nil {
			p.user.buf.Write(b[from:])
		}
	} else if p.user != nil {
		p.user.buf.Write(b)
	}
	for _, s := range p.agent {
		s.buf.Write(b)
		if s.fold != nil {
			show = s.fold.write(b)
		}
	}
	if p.tool != nil {
		show = p.tool.write(b)
	}
	p.emit(show)
}

// cleared marks in the journal where the user erased the screen: the
// session goes on, and the assistant sees only what is on the screen from
// here. Two clears in a row, or one with nothing before it, cut nothing more.
func (p *Proxy) cleared() {
	if es := p.sess.Entries(); len(es) > 0 && es[len(es)-1].Kind != session.KindClear {
		_ = p.sess.Append(session.Entry{Kind: session.KindClear})
	}
	p.folds = nil
	if p.user != nil {
		p.user.buf = capture.NewBuffer(headCap, tailCap)
		p.user.cleared = true
	}
}

func (p *Proxy) marker(m Marker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch m.Kind {
	case "cmd-start":
		p.user = &segment{cmd: m.Payload, buf: capture.NewBuffer(headCap, tailCap)}
	case "ask-start":
		p.asking = true
		p.folds = nil
		// A command that asks (an alias of __aish_ask) is the request's:
		// in the journal it would hold the whole reply as its output.
		p.user = nil
	case "cmd-end":
		defer p.drawStatus() // once the command is in the journal
		p.at = nil
		p.asking = false
		if p.tool != nil {
			p.finishFold(p.tool, 130)
			p.tool = nil
		}
		// Back at the prompt: an agent command interrupted with Ctrl+C never
		// sent agent-end. Keep what it printed for the next `agent start`.
		for id, seg := range p.agent {
			out, tui := render(seg.buf)
			p.finish(id, rpc.Output{Output: out, Exit: 130, TUI: tui})
			if seg.fold != nil {
				p.finishFold(seg.fold, 130)
			}
		}
		clear(p.agent)
		rc, cwd, _ := strings.Cut(m.Payload, ";")
		defer p.saveState(cwd) // with the command that changed it in the journal
		seg := p.user
		p.user = nil
		if p.switched {
			p.switched = false
			return // `aish resume` belongs to neither session
		}
		if seg == nil || strings.TrimSpace(seg.cmd) == "" {
			return
		}
		exit, _ := strconv.Atoi(rc)
		out, tui := render(seg.buf)
		if seg.cleared && strings.TrimSpace(out) == "" {
			return // `clear` itself: nothing left on the screen
		}
		if ignoredCommand(seg.cmd, p.ignore) {
			out = session.NotRecorded
		}
		_ = p.sess.Append(session.Entry{Kind: session.KindShell, Cmd: seg.cmd, Output: out, Exit: exit, Cwd: cwd, TUI: tui})
	case "agent-start":
		id, cmd, _ := strings.Cut(m.Payload, ";")
		seg := &segment{cmd: cmd, buf: capture.NewBuffer(headCap, tailCap)}
		if p.foldLines >= 0 {
			seg.fold = newFold("❯ "+cmd, p.foldLines)
			if p.foldLines == 0 {
				seg.fold.at = p.at
			}
		}
		p.at = nil
		p.agent[id] = seg
	case "agent-end":
		f := strings.SplitN(m.Payload, ";", 3)
		if len(f) < 3 {
			return
		}
		id := f[0]
		seg, ok := p.agent[id]
		if !ok {
			return
		}
		delete(p.agent, id)
		exit, _ := strconv.Atoi(f[1])
		if seg.fold != nil {
			p.finishFold(seg.fold, exit)
		}
		out, tui := render(seg.buf)
		p.finish(id, rpc.Output{Output: out, Exit: exit, Cwd: f[2], TUI: tui})
	}
}

// finish keeps an agent command's output for wait, waking the agent's
// Resume that may already be waiting for it.
func (p *Proxy) finish(id string, out rpc.Output) {
	p.done[id] = out
	if ch, ok := p.waiters[id]; ok {
		close(ch)
		delete(p.waiters, id)
	}
}

// finishFold prints the final status line and keeps the output for Ctrl+O.
func (p *Proxy) finishFold(f *fold, exit int) {
	p.emit(f.finish(exit))
	if f.keep() {
		p.folds = append(p.folds, Fold{Title: f.title, Text: string(f.raw.Bytes())})
	}
}

func render(b *capture.Buffer) (string, bool) {
	if b.AltScreen() {
		return "[full-screen interactive program; output not captured]", true
	}
	return capture.Clean(b.Bytes()), false
}

func (p *Proxy) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case rpc.MethodInfo:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.info(), nil
	case rpc.MethodModel:
		var mp rpc.ModelParams
		if err := json.Unmarshal(params, &mp); err != nil {
			return nil, err
		}
		if mp.Model == "" {
			return nil, errors.New("no model given")
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.asking {
			return nil, errors.New("the model is switched by the user, not by the assistant")
		}
		return p.switchModel(mp)
	case rpc.MethodStatus:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.status(), nil
	case rpc.MethodHistory:
		return p.session().Entries(), nil
	case rpc.MethodAgentStart, rpc.MethodAgentResume, rpc.MethodCompact:
		var ap rpc.AgentParams
		if err := json.Unmarshal(params, &ap); err != nil {
			return nil, err
		}
		switch method {
		case rpc.MethodAgentStart:
			return nil, p.agentStart(ctx, ap)
		case rpc.MethodAgentResume:
			return nil, p.agentResume(ctx, ap)
		}
		return nil, p.compact(ctx, ap)
	case rpc.MethodAgentCancel:
		p.cancelRequest()
		return nil, nil
	case rpc.MethodFolds:
		p.mu.Lock()
		defer p.mu.Unlock()
		return append([]Fold{}, p.folds...), nil
	case rpc.MethodClear:
		var cp rpc.ClearParams
		if len(params) > 0 { // none at all: plain `aish clear`
			if err := json.Unmarshal(params, &cp); err != nil {
				return nil, err
			}
		}
		return p.clear(cp)
	case rpc.MethodResume:
		var rp rpc.ResumeParams
		if err := json.Unmarshal(params, &rp); err != nil {
			return nil, err
		}
		return p.resume(rp.ID)
	case rpc.MethodMCPStatus:
		return p.mcp.Status(), nil
	case rpc.MethodMCPList:
		var lp mcp.ListParams
		if err := json.Unmarshal(params, &lp); err != nil {
			return nil, err
		}
		return p.mcp.List(ctx, lp.Wait), nil
	case rpc.MethodMCPCall:
		var cp mcp.CallParams
		if err := json.Unmarshal(params, &cp); err != nil {
			return nil, err
		}
		return p.mcp.Call(ctx, cp.Name, cp.Args)
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

func (p *Proxy) wait(ctx context.Context, id string, timeout time.Duration) (rpc.Output, error) {
	p.mu.Lock()
	if out, ok := p.done[id]; ok {
		delete(p.done, id)
		p.mu.Unlock()
		return out, nil
	}
	ch, ok := p.waiters[id]
	if !ok {
		ch = make(chan struct{})
		p.waiters[id] = ch
	}
	p.mu.Unlock()

	select {
	case <-ch:
	case <-time.After(timeout):
	case <-ctx.Done():
		// The agent is gone; any output stays for whoever asks next.
		p.mu.Lock()
		if p.waiters[id] == ch {
			delete(p.waiters, id)
		}
		p.mu.Unlock()
		return rpc.Output{}, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// The output may have come while the timer fired.
	out, ok := p.done[id]
	if !ok {
		if p.waiters[id] == ch {
			delete(p.waiters, id)
		}
		return rpc.Output{}, fmt.Errorf("no output recorded for %s", id)
	}
	delete(p.done, id)
	return out, nil
}
