package agent

import (
	"strings"
	"testing"
)

// A line that makes a name of a command run another program is refused
// even when its commands match the patterns and set no variable: the
// policy's "rebind" is refused last, after the reasons that name more.
func TestScopedBashRebind(t *testing.T) {
	dir := t.TempDir()
	s := &bashScope{patterns: []string{"git *", "hash *", "enable *"}}
	for cmd, want := range map[string]string{
		"hash -p /tmp/x git; git log": "(rebind)",
		"enable -f ./x.so git":        "(rebind)",
		"PATH=. git log":              "sets PATH",
	} {
		if why := refused(s, cmd, dir, nil); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q in it", cmd, why, want)
		}
	}
	if why := refused(s, "git log; hash -r", dir, nil); why != "" {
		t.Errorf("git log; hash -r refused: %s", why)
	}
}
