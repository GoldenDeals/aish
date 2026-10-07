package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/creack/pty"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/shellinit"
)

// startShell starts the user's bash in a PTY of its own, with the rc file
// of run (~/.bashrc, then init.bash) and the variables by which the shell
// and the commands in it find the proxy. shell is the one config.toml
// names, if any; self is aish's own path, sock the proxy's socket.
func (p *Proxy) startShell(sh *shellRun, shell, self, run, sock string) error {
	bash, err := bashPath(shell)
	if err != nil {
		return err
	}
	cmd := exec.Command(bash, "--rcfile", filepath.Join(run, "rc"), "-i")
	cmd.Env = append(os.Environ(),
		"AISH_SOCK="+sock,
		"AISH_RUN="+run,
		"AISH_BIN="+self,
		"AISH_SESSION="+p.sess.ID,
		"AISH_TOOLS_PATH="+filepath.Join(run, "bin"),
	)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	sh.cmd, sh.ptmx = cmd, ptmx
	sh.onExit(func() { _ = ptmx.Close() })
	p.setTerminal(ptmx)
	return nil
}

// makeRunDir creates the session's directory. Its bin, $AISH_TOOLS_PATH,
// holds aish when PATH has none: the function aish of init.bash is only
// the shell's, while a subagent's bash, hooks and external tools run as
// processes of their own, and the model calls MCP tools from bash as `aish
// tool NAME`. Tools and subcommands are not commands: bin comes first in
// PATH, and they would take names from the whole shell; who wants them
// short gives them aliases.
func makeRunDir(self, nonce string, route config.Route) (string, error) {
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
	if _, err := exec.LookPath("aish"); err != nil {
		script := fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", self)
		if err := os.WriteFile(filepath.Join(bin, "aish"), []byte(script), 0o755); err != nil {
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
