package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// A policy that asks about cd, but not into the directory the shell is
// already in, must see `cd <cwd>` as such however the model spells the
// directory, also when the shell got there through a symlink.
func TestCdIntoCwd(t *testing.T) {
	e := mustLoad(t, map[string]string{"cd.cedar": permitAll + `@ask("cd")
forbid(principal, action == Action::"run", resource == Command::"cd")
unless { context.paths == [context.cwd] };
`})
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{real, link} {
		for _, c := range []struct {
			cmd, want string
		}{
			{"cd .", Allow},
			{`cd "$PWD"`, Allow},
			{"cd ${PWD}/", Allow},
			{"cd " + cwd, Allow},
			{"cd " + real + " && ls", Allow},
			{"cd " + link, Allow},
			{"cd ..", Ask},
			{"cd $PWD/..", Ask},
			{"cd /", Ask},
		} {
			d := check(t, e, NewInput("bash", map[string]any{"command": c.cmd}, cwd))
			if d.Action != c.want {
				t.Errorf("in %s: %q: %s (%s), want %s", cwd, c.cmd, d.Action, d.Reason, c.want)
			}
		}
	}
}
