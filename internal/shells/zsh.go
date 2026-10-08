package shells

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GoldenDeals/aish/internal/shellinit"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// Zsh is zsh with init.zsh: `zsh -i` with ZDOTDIR=$AISH_RUN/zsh, whose
// .zshenv and .zshrc read the user's from where they would have been and
// then init.zsh. Path is the zsh configured, a name or a path.
type Zsh struct{ Path string }

func (Zsh) Name() string { return "zsh" }

func (Zsh) Files() map[string]string {
	return map[string]string{
		"zsh/.zshenv": shellinit.ZshEnv,
		"zsh/.zshrc":  shellinit.ZshRC(),
	}
}

func (z Zsh) Command(run string, env []string) (*exec.Cmd, error) {
	path, err := exec.LookPath(z.Path)
	if err != nil {
		return nil, fmt.Errorf("shell in config: %w", err)
	}
	// The user's ZDOTDIR, for .zshenv there to put back; none, none.
	var out []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "ZDOTDIR="); ok {
			out = append(out, "AISH_ZDOTDIR="+v)
			continue
		}
		if !strings.HasPrefix(kv, "AISH_ZDOTDIR=") {
			out = append(out, kv)
		}
	}
	cmd := exec.Command(path, "-i")
	cmd.Env = append(out, "ZDOTDIR="+filepath.Join(run, "zsh"))
	return cmd, nil
}

func (Zsh) ParseState(dump []byte, cwd string, ignore []string) (shellstate.State, error) {
	return shellstate.ParseZsh(dump, cwd, ignore)
}

func (Zsh) RestoreScript(d shellstate.State) string {
	return shellstate.Script(d.Of(shellstate.Zsh))
}
