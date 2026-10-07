package shells

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/GoldenDeals/aish/internal/shellinit"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// Bash is bash with init.bash: `bash --rcfile $AISH_RUN/rc -i`, the rc
// file sourcing ~/.bashrc first. Path is the bash configured, "" for the
// one of SHELL or of PATH.
type Bash struct{ Path string }

func (Bash) Name() string { return "bash" }

func (Bash) Files() map[string]string { return map[string]string{"rc": shellinit.RCFile()} }

func (b Bash) Command(run string, env []string) (*exec.Cmd, error) {
	bash, err := bashPath(b.Path)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bash, "--rcfile", filepath.Join(run, "rc"), "-i")
	cmd.Env = env
	return cmd, nil
}

func (Bash) ParseState(dump []byte, cwd string, ignore []string) (shellstate.State, error) {
	return shellstate.Parse(dump, cwd, ignore)
}

func (Bash) RestoreScript(d shellstate.State) string {
	return shellstate.Script(d.Of(""))
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
