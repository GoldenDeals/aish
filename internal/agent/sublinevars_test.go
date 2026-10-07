package agent

import (
	"strings"
	"testing"
)

// A line that sets HOME, PWD, OLDPWD or CDPATH takes its paths by them,
// which the policy cannot know: it is refused for a reason that names
// the variable.
func TestScopedBashLineVars(t *testing.T) {
	dir := t.TempDir()
	s := &bashScope{patterns: []string{"git *"}}
	for cmd, want := range map[string]string{
		"HOME=/tmp git log":        "sets HOME",
		"export CDPATH=/; git log": "sets CDPATH",
		`cd "$d" && git log`:       "(computed)",
	} {
		if why := refused(s, cmd, dir, nil); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q in it", cmd, why, want)
		}
	}
}
