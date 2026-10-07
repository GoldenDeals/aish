package proxy

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shells"
)

// Run starts the shell and blocks until it exits, returning its exit code.
// conf is the config files as aish read them as it started, cfg what they
// select for its environment: requests go by conf until `aish
// apply-config` reads them anew.
func (p *Proxy) Run(conf *config.Snapshot, cfg config.Config) (int, error) {
	p.configure(conf, cfg)
	sh := &shellRun{}
	defer sh.cleanup()
	if err := p.setup(sh, cfg); err != nil {
		return 1, err
	}
	return p.loop(sh)
}

// shellRun is the shell setup started for loop, and what is undone once it
// is gone.
type shellRun struct {
	cmd         *exec.Cmd
	ptmx        *os.File // the PTY's master side
	nonce       string   // of the markers, see NewFilter
	stopSignals func()   // see forwardSignals
	undo        []func()
}

// onExit has f run by cleanup: the last one given first, as deferred calls
// are.
func (sh *shellRun) onExit(f func()) { sh.undo = append(sh.undo, f) }

func (sh *shellRun) cleanup() {
	for i := len(sh.undo) - 1; i >= 0; i-- {
		sh.undo[i]()
	}
}

// configure takes the config aish started with: the snapshot requests go
// by, what the proxy takes from it for itself, and the shell's profile,
// model and effort, or those the resumed session kept.
func (p *Proxy) configure(conf *config.Snapshot, cfg config.Config) {
	p.mu.Lock()
	p.conf, p.started = conf, &cfg
	p.mcpFile, p.mcpSum = cfg.MCPConfig, fileSum(cfg.MCPConfig)
	p.applyFields(cfg)
	p.mu.Unlock()
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
}

// setup starts the shell for loop: it takes the terminal, locks the
// session, makes $AISH_RUN, starts the MCP servers and the socket, then
// the shell in its PTY (startShell). What it did, sh.cleanup undoes in
// reverse, also when it fails halfway.
func (p *Proxy) setup(sh *shellRun, cfg config.Config) error {
	kind, err := shells.For(cfg.Shell)
	if err != nil {
		return err
	}
	p.shell = kind
	p.out = os.Stdout
	p.size = func() (int, int) {
		w, h, err := term.GetSize(int(os.Stdin.Fd()))
		if err != nil || w == 0 || h == 0 {
			return 80, 24
		}
		return w, h
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("aish must be started from a terminal")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := p.sess.Lock(); err != nil {
		return err
	}
	sh.onExit(func() { p.session().Unlock() })
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
	sh.nonce = rand.Text()
	run, err := makeRunDir(self, sh.nonce, cfg.Route, p.shell.Files())
	if err != nil {
		return err
	}
	sh.onExit(func() { _ = os.RemoveAll(run) })
	p.run = run
	if p.resumed != nil {
		script := p.shell.RestoreScript(p.resumed.Shell)
		if err := os.WriteFile(filepath.Join(run, "restore.bash"), []byte(script), 0o600); err != nil {
			return err
		}
	}

	servers, err := mcp.LoadConfig(cfg.MCPConfig)
	if err != nil {
		// A broken MCP config must not keep the shell from starting.
		fmt.Fprintf(os.Stderr, "aish: %v\n", err)
	}
	p.mcp = mcp.NewManager(servers, filepath.Join(config.CacheDir(), "mcp"))
	p.mcp.Warm()
	sh.onExit(p.mcp.Close)

	sock := filepath.Join(run, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	sh.onExit(func() { _ = l.Close() })
	go rpc.Serve(l, p.handle)

	if err := p.startShell(sh, self, run, sock); err != nil {
		return err
	}
	return p.takeTerminal(sh)
}

// takeTerminal has the shell follow the terminal's size and aish's
// signals, and puts the terminal in raw mode: from now on the keys go to
// the shell, or to the proxy, as they come.
func (p *Proxy) takeTerminal(sh *shellRun) error {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			_ = pty.InheritSize(os.Stdin, sh.ptmx)
			p.resized()
		}
	}()
	winch <- syscall.SIGWINCH
	sh.onExit(func() { signal.Stop(winch) })

	hup := make(chan os.Signal, 2)
	signal.Notify(hup, syscall.SIGHUP, syscall.SIGTERM)
	sh.onExit(func() { signal.Stop(hup) })
	sh.stopSignals = forwardSignals(hup, sh.cmd.Process, shutdownGrace)

	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// bash is running already. A bash that traps the hangup of the
		// ptmx.Close in cleanup would outlive aish without its $AISH_RUN,
		// which cleanup removes.
		sh.stopSignals()
		if err := syscall.Kill(-sh.cmd.Process.Pid, syscall.SIGKILL); err != nil {
			_ = sh.cmd.Process.Kill()
		}
		_ = sh.cmd.Wait()
		return err
	}
	sh.onExit(func() { _ = term.Restore(int(os.Stdin.Fd()), old) })
	return nil
}

// loop runs the shell setup started until it exits: the keys go to it, and
// its output to the recorder and the terminal.
func (p *Proxy) loop(sh *shellRun) (int, error) {
	p.holdEarly(sh.ptmx) // before the output is read: no prompt has gone by
	go p.input(os.Stdin, sh.ptmx)

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		p.pump(sh.ptmx, NewFilter(sh.nonce))
	}()

	waitErr := sh.cmd.Wait()
	sh.stopSignals()
	// Drain whatever bash wrote last; the PTY reports EIO once it is gone.
	select {
	case <-outDone:
	case <-time.After(200 * time.Millisecond):
	}
	p.restoreScreen()
	// Before cleanup: their commands are in process groups of their own
	// and would outlive aish.
	p.stopBackground()
	return exitCode(waitErr)
}
