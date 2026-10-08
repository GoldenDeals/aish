// Package shells is the shell aish runs in its PTY: how it starts, with the
// integration script of shellinit after the user's own configuration, and
// how its state, as the script dumps it at every prompt, is read and
// brought back (shellstate). The proxy is the same for every shell: the
// markers, $AISH_RUN with its files and `aish agent start|resume` are a
// contract each integration script keeps (the head of init.bash).
package shells

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GoldenDeals/aish/internal/shellstate"
)

// Shell is one kind of shell.
type Shell interface {
	// Name is what the config, the system prompt and the policy call the
	// shell: "bash".
	Name() string
	// Files are what the shell reads as it starts, by their path in
	// $AISH_RUN.
	Files() map[string]string
	// Command is the interactive shell to run in the PTY, with run for
	// $AISH_RUN and env for its environment.
	Command(run string, env []string) (*exec.Cmd, error)
	// ParseState reads what the integration script dumped (state,
	// state.base): variables matching a pattern of ignore are left out.
	ParseState(dump []byte, cwd string, ignore []string) (shellstate.State, error)
	// RestoreScript makes the change d in the shell that sources it at its
	// next prompt (restore.bash). Of a change of another kind it brings
	// back the directory alone.
	RestoreScript(d shellstate.State) string
}

// For is the shell configured, shell in config.toml: a name or a path of
// a bash, or "" for the bash of SHELL or of PATH. A name of another shell
// is an error.
func For(configured string) (Shell, error) {
	if base := filepath.Base(configured); strings.HasPrefix(base, "fish") {
		return nil, fmt.Errorf("shell in config: %s is not supported, only bash", configured)
	}
	return Bash{Path: configured}, nil
}

// Kind is the name of the shell configured, as For picks it; "" for one
// it does not.
func Kind(configured string) string {
	if sh, err := For(configured); err == nil {
		return sh.Name()
	}
	return ""
}
