package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// cdTree makes root/home, root/outside/dir and the link root/home/link to
// root/outside/dir, where home/link/.. is home for cd and outside for the
// kernel.
func cdTree(t *testing.T) (root string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"home", "outside/dir"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "outside", "dir"), filepath.Join(root, "home", "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// cd goes by the logical path unless told -P, and falls back to the path
// as spelled when the logical one fails: the paths of cd are both places
// it may enter.
func TestAnalyzeCdLogical(t *testing.T) {
	root := cdTree(t)
	home, outside := root+"/home", root+"/outside"
	both, physical := []string{home, outside}, []string{outside}
	for _, c := range []struct {
		argv []string
		want []string
	}{
		{[]string{"cd", "home/link/.."}, both},
		{[]string{"cd", "-P", "home/link/.."}, physical},
		{[]string{"ls", "home/link/.."}, physical},
		{[]string{"cd", "-L", "home/link/.."}, both},
		{[]string{"cd", "--", "home/link/.."}, both},
		{[]string{"cd", "~/link/.."}, both},
		{[]string{"cd", root + "/home/link/.."}, both},
		{[]string{"/usr/bin/cd", "home/link/.."}, both},
		{[]string{"pushd", "home/link/.."}, both},
		{[]string{"pushd", "-n", "home/link/.."}, both},
		{[]string{"pushd", "-P", "home/link/.."}, both},
		// The last of -L and -P counts, as in bash.
		{[]string{"cd", "-LP", "home/link/.."}, physical},
		{[]string{"cd", "-PL", "home/link/.."}, both},
		{[]string{"cd", "-P", "-L", "home/link/.."}, both},
		{[]string{"cd", "-e", "-P", "home/link/.."}, physical},
		// An option after the operand is not an option to cd.
		{[]string{"cd", "home/link/..", "-P"}, both},
		{[]string{"cd", "--", "-P", "home/link/.."}, []string{root + "/-P", home, outside}},
		// Both ways lead to the same place: one path.
		{[]string{"cd", "home/link"}, []string{outside + "/dir"}},
		{[]string{"cd", "home/link/../.."}, []string{root}},
		{[]string{"cd", "outside/dir/.."}, physical},
		{[]string{"cd", "home/missing/.."}, []string{home}},
	} {
		if got := Analyze(c.argv, root, home).Paths; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: paths %q, want %q", c.argv, got, c.want)
		}
	}
}

// A rule against cd into a directory holds for a path that reaches it
// through a link and "..".
func TestPolicyCdLogical(t *testing.T) {
	root := cdTree(t)
	e := mustLoad(t, map[string]string{"cd.cedar": permitAll + fmt.Sprintf(`@reason("cd home")
forbid(principal, action == Action::"run", resource == Command::"cd")
when { context.paths.contains(%q) };
`, root+"/home")})
	for _, c := range []struct{ cmd, want string }{
		{"cd ./home", Deny},
		{"cd home/link/..", Deny},
		{"cd home/link/.. && ls", Deny},
		{"builtin cd home/link/..", Deny},
		{"cd -P home/link/..", Allow},
		{"cd ./outside", Allow},
	} {
		d := check(t, e, callInput("bash", map[string]any{"command": c.cmd}, root))
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}
