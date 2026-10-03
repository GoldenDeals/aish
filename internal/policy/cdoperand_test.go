package policy

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// The operand of cd and pushd is a directory however it is spelled, and cd
// alone goes home.
func TestCdOperand(t *testing.T) {
	root := cdTree(t)
	home := root + "/home"
	for _, c := range []struct {
		argv []string
		want []string
	}{
		{[]string{"cd", "home"}, []string{home}},
		{[]string{"cd", "--", "home"}, []string{home}},
		{[]string{"pushd", "home"}, []string{home}},
		{[]string{"pushd", "-n", "home"}, []string{home}},
		{[]string{"cd", "home/link"}, []string{root + "/outside/dir"}},
		{[]string{"cd", "~+/home"}, []string{home}},
		{[]string{"cd"}, []string{home}},
		{[]string{"cd", "-L"}, []string{home}},
		{[]string{"cd", "-"}, nil},
		{[]string{"popd"}, nil},
		{[]string{"ls", "home"}, nil},
	} {
		if got := Analyze(c.argv, root, home).Paths; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: paths %q, want %q", c.argv, got, c.want)
		}
	}
	// Without a HOME cd alone fails: it goes nowhere.
	if got := Analyze([]string{"cd"}, root, ""); got.Paths != nil || got.lost {
		t.Errorf("cd without HOME: paths %q, lost %v", got.Paths, got.lost)
	}
}

// A cd no policy can follow before the line runs is marked as code built
// at run time.
func TestCdLost(t *testing.T) {
	root := cdTree(t)
	for _, c := range []struct {
		cmd  string
		lost bool
	}{
		{"cd -", true},
		{"cd -P -", true},
		{"cd -- -", true},
		{"pushd -", true},
		{"cd ~-", true},
		{"cd ~-/x", true},
		{"cd ~1", true},
		{"cd ~+2", true},
		{`cd "$d"`, true},
		{"cd $(git rev-parse --show-toplevel)", true},
		{"cd $HOME/$d", true},
		{"cd hom?", true},
		{"popd", true},
		{"popd +1", true},
		{"pushd", true},
		{"pushd +1", true},
		{"pushd -1", true},
		{"ls && cd -", true},
		{"bash -c 'cd -'", true},

		{"cd", false},
		{"cd home", false},
		{"cd ~", false},
		{"cd ~+", false},
		{"cd $HOME/x", false},
		{`cd "$PWD"`, false},
		{"pushd home", false},
		{"pushd -n", false},
		{"pushd -n +1", false},
		{"popd -n", false},
		{"ls -", false},
		{"ls ~-", false},
	} {
		in := callInput("bash", map[string]any{"command": c.cmd}, root)
		if got := slices.Contains(in.Dynamic, "computed"); got != c.lost {
			t.Errorf("%s: dynamic %q, want computed %v", c.cmd, in.Dynamic, c.lost)
		}
	}
}

// pwdTree makes root/a/link to root/b/c: the shell that entered it by the
// link is in root/a/link for cd, and in root/b/c for the kernel.
func pwdTree(t *testing.T) (root string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a", "b/c"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "b", "c"), filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// cd .. goes up from the directory as the shell names it, PWD, which is
// not where the link leads: the paths are both.
func TestCdLogicalCwd(t *testing.T) {
	root := pwdTree(t)
	link, real := root+"/a/link", root+"/b/c"
	both := []string{root + "/a", root + "/b"}
	for _, c := range []struct {
		cwd string
		env []string
	}{
		{real, []string{"PWD=" + link}},
		{link, nil},
		{link, []string{"PWD=" + link + "/"}},
		// A PWD naming another directory is not the shell's.
		{link, []string{"PWD=" + root}},
	} {
		for _, k := range []struct {
			cmd  string
			want []string
		}{
			{"cd ..", both},
			{"pushd ..", both},
			{"cd $PWD/..", both},
			{"cd ~+/..", both},
			{"cd ../c", []string{root + "/a/c", real}},
			{"cd -P ..", []string{root + "/b"}},
			{"ls ..", []string{root + "/b"}},
			{"cd .", []string{real}},
		} {
			in := NewInput("bash", map[string]any{"command": k.cmd}, c.cwd, append([]string{"HOME=/nonexistent"}, c.env...))
			in.HandOff(k.cmd)
			if in.Cwd != real {
				t.Fatalf("cwd %s, want %s", in.Cwd, real)
			}
			if got := in.Analyze(in.Commands[0]).Paths; !reflect.DeepEqual(got, k.want) {
				t.Errorf("cwd %s, env %q: %s: paths %q, want %q", c.cwd, c.env, k.cmd, got, k.want)
			}
		}
	}

	e := mustLoad(t, map[string]string{"cd.cedar": permitAll + fmt.Sprintf(`@reason("cd a")
forbid(principal, action == Action::"run", resource)
when { context.paths.contains(%q) };
`, root+"/a")})
	for _, c := range []struct{ cmd, want string }{
		{"cd ..", Deny},
		{"pushd ..", Deny},
		{"cd -P ..", Allow},
		{"ls ..", Allow},
	} {
		d := check(t, e, callInput("bash", map[string]any{"command": c.cmd}, link))
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}

	// A file tool takes a relative path from the directory as given, as
	// the tool itself does: ../x from the link is next to it.
	if got := NewInput("write_file", map[string]any{"path": "../x"}, link, nil).Path; got != root+"/a/x" {
		t.Errorf("write_file ../x from the link: %s, want %s", got, root+"/a/x")
	}
}

// A relative directory is looked up in CDPATH first, then in the current
// one; ., .. and a path starting with them or / are not.
func TestCdPath(t *testing.T) {
	root := cdTree(t)
	home := root + "/home"
	env := []string{"HOME=" + home, "CDPATH=" + root + "/outside::home"}
	for _, c := range []struct {
		cmd  string
		want []string
	}{
		{"cd dir", []string{root + "/outside/dir", root + "/dir", root + "/home/dir"}},
		{"pushd dir", []string{root + "/outside/dir", root + "/dir", root + "/home/dir"}},
		// Below a name that is not there, .. goes back up as spelled; in
		// home, it goes by the link.
		{"cd link/..", []string{root + "/outside", root, home}},
		{"cd ./dir", []string{root + "/dir"}},
		{"cd ..", []string{filepath.Dir(root)}},
		{"cd " + root, []string{root}},
		{"cd ~", []string{home}},
	} {
		in := NewInput("bash", map[string]any{"command": c.cmd}, root, env)
		in.HandOff(c.cmd)
		got := in.Analyze(in.Commands[0]).Paths
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		want = slices.Compact(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: paths %q, want %q", c.cmd, got, want)
		}
	}
}

// A rule against cd into a directory holds however cd is told to go there.
func TestPolicyCdOperand(t *testing.T) {
	root := cdTree(t)
	home := root + "/home"
	t.Setenv("HOME", home)
	e := mustLoad(t, map[string]string{"cd.cedar": permitAll + fmt.Sprintf(`@reason("cd home")
forbid(principal, action == Action::"run", resource == Command::"cd")
when { context.paths.contains(%q) };
`, home)})
	for _, c := range []struct{ cmd, want string }{
		{"cd home", Deny},
		{"cd ./home", Deny},
		{"cd home/", Deny},
		{"cd outside", Allow},
	} {
		d := check(t, e, callInput("bash", map[string]any{"command": c.cmd}, root))
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
	for _, c := range []struct{ cmd, want string }{
		{"cd", Deny},
		{"cd ~", Deny},
		{"cd ..", Deny},
		{"cd dir", Allow},
	} {
		d := check(t, e, callInput("bash", map[string]any{"command": c.cmd}, root+"/home/link"))
		if d.Action != c.want {
			t.Errorf("in home/link: %s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
	// Through CDPATH, from anywhere.
	in := NewInput("bash", nil, root+"/outside", []string{"HOME=/nonexistent", "CDPATH=" + root})
	in.HandOff("cd home")
	if d := check(t, e, in); d.Action != Deny {
		t.Errorf("cd home with CDPATH: %s, want deny", d.Action)
	}
}

// ~ and $HOME in a command are HOME of the shell that runs it: after
// export HOME=/etc, > ~/passwd writes /etc/passwd. The home of the
// policies, Dir::"~" and write_outside_home, stays the user's.
func TestShellHome(t *testing.T) {
	ctx := context.Background()
	home, out := links(t, nil)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	env := []string{"HOME=" + out}
	in := NewInput("bash", nil, home, env)
	in.HandOff("cp x ~/y; echo x > ~/passwd; cd")
	if in.Home != home {
		t.Errorf("home %s, want %s", in.Home, home)
	}
	if want := []string{out + "/passwd"}; !reflect.DeepEqual(in.Writes, want) {
		t.Errorf("writes %q, want %q", in.Writes, want)
	}
	if got, want := in.Analyze(in.Commands[0]).Paths, []string{out + "/y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cp x ~/y: paths %q, want %q", got, want)
	}
	if got, want := in.Analyze(in.Commands[2]).Paths, []string{out}; !reflect.DeepEqual(got, want) {
		t.Errorf("cd: paths %q, want %q", got, want)
	}

	e, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: Deny})
	if err != nil {
		t.Fatal(err)
	}
	in = NewInput("bash", nil, home, env)
	in.HandOff("echo x > ~/passwd")
	if d := check(t, e, in); d.Action != Deny {
		t.Errorf("> ~/passwd with HOME=%s: %s, want deny", out, d.Action)
	}
	in = NewInput("bash", nil, home, []string{"HOME=" + home})
	in.HandOff("echo x > ~/passwd")
	if d := check(t, e, in); d.Action != Allow {
		t.Errorf("> ~/passwd: %s (%s), want allow", d.Action, d.Reason)
	}

	// The guard judges the path the command writes, too.
	data := filepath.Join(home, ".local", "share", "aish")
	in = NewInput("bash", nil, home, []string{"HOME=" + data})
	in.HandOff("cp /tmp/x ~/trusted.json")
	if d := check(t, e, in); d.Action != Deny || d.Reason != TrustReason {
		t.Errorf("cp to ~/trusted.json with HOME=%s: %+v", data, d)
	}

	// No HOME: ~ is not known, and cd alone goes nowhere.
	in = NewInput("bash", nil, home, []string{})
	in.HandOff("echo x > ~/x; cd")
	if in.Writes != nil || !slices.Equal(in.Dynamic, []string{"computed"}) {
		t.Errorf("without HOME: writes %q, dynamic %q", in.Writes, in.Dynamic)
	}
	if got := in.Analyze(in.Commands[1]).Paths; got != nil {
		t.Errorf("cd without HOME: paths %q", got)
	}
}

// A word starting with ~user is that user's home, and with a quoted ~ or
// $ the word may be the name as written: the paths are both then.
func TestTildeWords(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	const home, cwd = "/home/me", "/w"
	for _, c := range []struct {
		cmd  string
		want []string
	}{
		{"cp x ~" + me.Username + "/y", []string{walk(me.HomeDir + "/y")}},
		{"cp x ~no-such-user-aish/y", []string{cwd + "/~no-such-user-aish/y"}},
		{"cp x ~+/y", []string{cwd + "/y"}},
		{"cp x ~-/y", nil},
		{"cp x ~2/y", nil},
		{"cp x ~/y", []string{home + "/y"}},
		{`cp x "~/y"`, []string{home + "/y", cwd + "/~/y"}},
		{`cp x \~/y`, []string{home + "/y", cwd + "/~/y"}},
		{`cp x '$HOME/y'`, []string{home + "/y", cwd + "/$HOME/y"}},
		{`cp x "$HOME/y"`, []string{home + "/y"}},
		// After the directory a word is kept as spelled, for the guard.
		{`cp x $HOME/$y`, []string{home + "/$y"}},
		{`cp x ~/$y`, []string{home + "/$y"}},
		{`cp x $D/y`, nil},
	} {
		in := Input{Tool: "bash", Cwd: cwd, Home: home}
		in.HandOff(c.cmd)
		if got := in.Analyze(in.Commands[0]).Paths; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: paths %q, want %q", c.cmd, got, c.want)
		}
	}
}

// ~- is $OLDPWD, which may be the trust file's directory: the guard cannot
// see where it leads, and a line naming trusted.json through it is denied.
func TestGuardTildeOldpwd(t *testing.T) {
	ctx := context.Background()
	home, _ := links(t, nil)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	e, err := Load(ctx, t.TempDir(), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{"cp /tmp/x ~-/trusted.json", Deny},
		{"cp /tmp/x ~1/trusted.json", Deny},
		{"cp /tmp/x ~-/notes.txt", Allow},
		{"cp /tmp/x ~/notes.txt", Allow},
	} {
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, "/tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}
