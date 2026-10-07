package agent

import (
	"strings"
	"testing"
)

// The policy has the code of GIT_SSH_COMMAND=… among the commands of the
// line and marks a value made at run time computed: a scoped subagent is
// still told the variable the line sets, not a command of its value or
// the run time.
func TestScopedBashCodeVars(t *testing.T) {
	dir := t.TempDir()
	git := &bashScope{patterns: []string{"git *", "env *", "crontab *"}}
	for cmd, want := range map[string]string{
		"GIT_SSH_COMMAND='sudo ls' git fetch":      "sets GIT_SSH_COMMAND",
		`PAGER="$p" git log`:                       "sets PAGER",
		"export EDITOR='rm -rf x'; git commit":     "sets EDITOR",
		"env EDITOR='sudo ls' crontab -e":          "sets EDITOR",
		"LD_PRELOAD=/tmp/x.so git log":             "sets LD_PRELOAD",
		"for PAGER in 'sudo ls'; do git log; done": "sets PAGER",
		`env "$v"=. git log`:                       "computed",
	} {
		if why := refused(git, cmd, dir, nil); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q in it", cmd, why, want)
		}
	}
}
