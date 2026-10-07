package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// bash globs a word with its brackets out of quotes even when the text
// between them is quoted: /etc/pass['w']d is the glob /etc/pass[w]d, and
// names /etc/passwd. The files it matches are among the paths, as for a
// glob without quotes; the quoted text in a bracket expression stands for
// itself, ! and ^ and - with it. Quoted brackets are no glob.
func TestLinePathsQuotedGlob(t *testing.T) {
	root := lineTree(t)
	work, etc, me := root+"/work", root+"/etc", root+"/home/me"
	for _, f := range []string{"pass!d", "pass-d"} {
		if err := os.WriteFile(filepath.Join(etc, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		cmd, text string
		want      []string
		computed  bool
	}{
		{"cp x " + etc + "/pass['w']d", "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d", etc + "/passwd"}, false},
		{"cp x " + etc + `/pass["w"]d`, "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d", etc + "/passwd"}, false},
		{"cp x " + etc + "/pass[''w]d", "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d", etc + "/passwd"}, false},
		{"cp x " + etc + "/pass[']'w]d", "cp x " + etc + "/pass[]w]d", []string{etc + "/pass[]w]d", etc + "/passwd"}, false},
		{"cp x " + root + "/['e']tc/pass['w']d", "cp x " + root + "/[e]tc/pass[w]d", []string{root + "/[e]tc/pass[w]d", etc + "/passwd"}, false},
		{"cp x ~/['n']otes.txt", "cp x ~/[n]otes.txt", []string{me + "/[n]otes.txt", me + "/notes.txt"}, false},
		{"cp x ../etc/pass['w']d", "cp x ../etc/pass[w]d", []string{etc + "/pass[w]d", etc + "/passwd"}, false},
		{"cd " + etc + " && cp x pass['w']d", "cp x pass[w]d", []string{etc + "/x", etc + "/passwd"}, false},
		// Quoted, ! and ^ do not negate the bracket expression, nor does -
		// make a range in it.
		{"cp x " + etc + "/pass['!w']d", "cp x " + etc + "/pass[!w]d", []string{etc + "/pass[!w]d", etc + "/pass!d", etc + "/passwd"}, false},
		{"cp x " + etc + "/pass['^x']d", "cp x " + etc + "/pass[^x]d", []string{etc + "/pass[^x]d"}, true},
		{"cp x " + etc + "/pass[a'-'z]d", "cp x " + etc + "/pass[a-z]d", []string{etc + "/pass[a-z]d", etc + "/pass-d"}, false},
		// A glob matching nothing in a directory that is there is marked.
		{"cp x " + etc + "/nothing['x']", "cp x " + etc + "/nothing[x]", []string{etc + "/nothing[x]"}, true},

		// As before: a glob out of quotes, and one quoted whole or with
		// its brackets quoted.
		{"cp x " + etc + "/pass[w]d", "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d", etc + "/passwd"}, false},
		{"cp x '" + etc + "/pass[w]d'", "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d"}, false},
		{"cp x " + etc + "/pass'['w]d", "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d"}, false},
		{"cp x " + etc + "/pass[w']'d", "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d"}, false},
		{"cp x " + etc + `/pass\[w]d`, "cp x " + etc + "/pass[w]d", []string{etc + "/pass[w]d"}, false},
		// A bare word in the shell's own directory is a name.
		{"cp x pass['w']d", "cp x pass[w]d", nil, false},
	} {
		in := lineInput(c.cmd, work, me)
		if got := commandPaths(t, in, c.text); !slices.Equal(got, sorted(c.want...)) {
			t.Errorf("%s: paths %q, want %q", c.cmd, got, sorted(c.want...))
		}
		if got := slices.Contains(in.Dynamic, "computed"); got != c.computed {
			t.Errorf("%s: dynamic %q, want computed %v", c.cmd, in.Dynamic, c.computed)
		}
	}
}

// The criterion of the task: a rule on /etc/passwd holds for a glob with
// quotes between its brackets.
func TestLinePathsQuotedGlobPolicy(t *testing.T) {
	root := lineTree(t)
	etc := root + "/etc"
	e := mustLoad(t, map[string]string{"etc.cedar": permitAll + fmt.Sprintf(`@reason("passwd")
forbid(principal, action == Action::"run", resource)
when { context.paths.contains(%q) };
`, etc+"/passwd")})
	for _, c := range []struct{ cmd, want string }{
		{"cp x " + etc + "/pass['w']d", Deny},
		{"cp x " + etc + `/pass["w"]d`, Deny},
		{"cp x " + etc + "/pass['!x']d", Allow},
		{"cp x " + etc + "/pass['!w']d", Deny},
		{"cp x " + etc + "/pass[a'-'z]d", Allow},
		{"cp x " + etc + "/p['a']s{s,x}['w']d", Deny},
		{"cd " + etc + " && cp x pass['w']d", Deny},
		{"env -C " + etc + " cp x pass['w']d", Deny},
		{"cp x " + etc + "/pass[w]d", Deny},
		{"cp x '" + etc + "/pass[w]d'", Allow},
		{"cp x " + etc + "/pass'['w]d", Allow},
	} {
		if d := check(t, e, lineInput(c.cmd, root+"/work", root+"/home/me")); d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}

// The guard sees the trust file in a glob with quotes between its
// brackets.
func TestGuardQuotedGlob(t *testing.T) {
	ctx := context.Background()
	home, _ := links(t, nil)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	data := filepath.Join(home, ".local", "share", "aish")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "trusted.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := Load(ctx, t.TempDir(), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{"cp /tmp/x ~/.local/share/aish/trusted['.']json", Deny},
		{"cp /tmp/x ~/.local/share/a['i']sh/trusted.json", Deny},
		{"cd ~/.local/share/aish && cp /tmp/x trusted['.']json", Deny},
		{"cp /tmp/x ~/['n']otes.json", Allow},
	} {
		d, err := e.Check(ctx, lineInput(c.cmd, home, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}
