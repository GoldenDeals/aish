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
// matched by the commands before the error, whole, as HISTIGNORE would, and
// by each of its lines.
func ignoredCommand(line string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	cmds, err := policy.Commands(line)
	if err != nil || len(cmds) == 0 {
		cmds = append(cmds, []string{strings.TrimSpace(line)})
	}
	if err != nil {
		// bash runs a paste line by line, up to the error, and a line past
		// it may be a command the parse never got to. Matching one bash
		// does not run costs its output; missing one, a secret in the
		// journal.
		for l := range strings.SplitSeq(line, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				cmds = append(cmds, []string{l})
			}
		}
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
