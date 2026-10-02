package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A command hands its paths to the kernel as spelled, and there a ".."
// goes up from where the symlink before it really leads: cp x ~/link/../y
// writes next to the link's target, not into home.
func TestAnalyzeDotDotAfterLink(t *testing.T) {
	home, out := links(t, map[string]string{
		"link": "$out/deep/er",
		"up":   "../out",
	})
	if err := os.Mkdir(filepath.Join(home, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(home)
	for _, c := range []struct{ dst, want string }{
		{home + "/link/../y", out + "/deep/y"},
		{"~/link/../y", out + "/deep/y"},
		{"${HOME}/link/../y", out + "/deep/y"},
		{"$PWD/link/../y", out + "/deep/y"},
		{"link/../y", out + "/deep/y"},
		{"./link/./../y", out + "/deep/y"},
		{"link/../../y", out + "/y"},
		{"up/../y", root + "/y"},
		// Without a link on the way it is the spelling, as before.
		{"real/../y", home + "/y"},
		{"missing/../y", home + "/y"},
	} {
		got := Analyze([]string{"cp", "x", c.dst}, home, home).Paths
		if want := []string{c.want}; !reflect.DeepEqual(got, want) {
			t.Errorf("cp x %s: paths %q, want %q", c.dst, got, want)
		}
	}
	if got := Analyze([]string{"cp", "x", "$D/link/../y"}, home, home).Paths; len(got) != 0 {
		t.Errorf("cp x $D/link/../y: paths %q, want none", got)
	}
}

// A Cedar rule on the paths of a command sees the path the command writes,
// for a call as the agent makes it.
func TestPolicyDotDotAfterLink(t *testing.T) {
	home, out := links(t, map[string]string{"link": "$out/deep/er"})
	t.Setenv("HOME", home)
	e := mustLoad(t, map[string]string{"out.cedar": permitAll + fmt.Sprintf(`@reason("writes out")
forbid(principal, action == Action::"run", resource == Command::"cp")
when { context.paths.contains(%q) };
`, out+"/deep/y")})
	for _, c := range []struct{ cmd, want string }{
		{"cp x ~/link/../y", Deny},
		{"cp x link/../y", Deny},
		{"cd /tmp && cp x " + home + "/link/../y", Deny},
		{"cp x ~/y", Allow},
	} {
		d := check(t, e, callInput("bash", map[string]any{"command": c.cmd}, home))
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}
