package shells

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/shellstate"
)

// TestFor: shell in config.toml picks the shell by the name of its
// program; one aish has no integration for is an error, not a bash run
// with the arguments of another.
func TestFor(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{
		{"", "bash"},
		{"bash", "bash"},
		{"/opt/bash/bin/bash", "bash"},
	} {
		sh, err := For(tc.configured)
		if err != nil || sh.Name() != tc.want {
			t.Errorf("%q: %v, %v; want %s", tc.configured, sh, err, tc.want)
		}
		if got := Kind(tc.configured); got != tc.want {
			t.Errorf("Kind(%q) = %s, want %s", tc.configured, got, tc.want)
		}
	}
	for _, bad := range []string{"fish", "/usr/bin/fish"} {
		if _, err := For(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// TestRestoreOtherKind: the state of another shell is code this one does
// not read; only its directory comes back.
func TestRestoreOtherKind(t *testing.T) {
	d := shellstate.State{Kind: "other", Vars: map[string]string{"X": "X=1"}, Cwd: "/srv"}
	got := Bash{}.RestoreScript(d)
	if strings.Contains(got, "X=1") || !strings.Contains(got, "/srv") {
		t.Errorf("script:\n%s", got)
	}
}
