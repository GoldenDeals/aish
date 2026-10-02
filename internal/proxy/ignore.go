package proxy

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/inebotov/aish/internal/policy"
)

// ignoredCommand reports whether a command line the user typed matches a
// journal_ignore pattern. Each simple command of the line is matched on its
// own, the way the policy sees them, so that `sudo env`, `env | grep X` and
// `bash -c env` are caught by the pattern `env`; a line bash cannot parse is
// matched whole, as HISTIGNORE would.
func ignoredCommand(line string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	cmds, err := policy.Commands(line)
	if err != nil || len(cmds) == 0 {
		cmds = [][]string{{strings.TrimSpace(line)}}
	}
	for _, argv := range cmds {
		cmd := strings.Join(argv, " ")
		short := strings.Join(append([]string{filepath.Base(argv[0])}, argv[1:]...), " ")
		for _, pat := range patterns {
			if matchCommand(pat, cmd) || matchCommand(pat, short) {
				return true
			}
		}
	}
	return false
}

// matchCommand is path.Match over the whole command, where `*` must cross
// slashes too (`cat *credentials*` against `cat ~/.aws/credentials`): both
// sides have their slashes swapped for a byte neither can hold.
func matchCommand(pattern, cmd string) bool {
	ok, _ := path.Match(strings.ReplaceAll(pattern, "/", "\x00"), strings.ReplaceAll(cmd, "/", "\x00"))
	return ok
}
