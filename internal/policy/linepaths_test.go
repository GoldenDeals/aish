package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// lineTree makes root/{home/me,etc/ssh,work}, root/etc/passwd and
// root/work/link to root/etc/ssh.
func lineTree(t *testing.T) (root string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"home/me", "etc/ssh", "work"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"etc/passwd", "etc/ssh/keys", "home/me/notes.txt", "home/me/todo.txt"} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "etc", "ssh"), filepath.Join(root, "work", "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// lineInput is the input of a bash call of cmd from cwd, with HOME home.
func lineInput(cmd, cwd, home string, env ...string) Input {
	in := NewInput("bash", map[string]any{"command": cmd}, cwd, append([]string{"HOME=" + home}, env...))
	in.HandOff(cmd)
	return in
}

// commandPaths is Paths of the command of in whose text is text.
func commandPaths(t *testing.T, in Input, text string) []string {
	t.Helper()
	for i := range in.Commands {
		if c := in.command(i); c.Text == text {
			got := slices.Clone(c.Paths)
			slices.Sort(got)
			return got
		}
	}
	t.Fatalf("%s: no command %q in %q", in.Line, text, in.Commands)
	return nil
}

func sorted(ss ...string) []string {
	ss = slices.Clone(ss)
	slices.Sort(ss)
	return slices.Compact(ss)
}

// The criterion of the task, on the machine's own /etc and /home.
func TestLinePathsCriterion(t *testing.T) {
	cwd := t.TempDir()
	in := lineInput("cd /etc && cp x passwd", cwd, "/nonexistent")
	if got := commandPaths(t, in, "cp x passwd"); !slices.Contains(got, walk("/etc/passwd")) {
		t.Errorf("cd /etc && cp x passwd: paths %q, want /etc/passwd among them", got)
	}
	if len(in.Dynamic) != 0 {
		t.Errorf("cd /etc && cp x passwd: dynamic %q", in.Dynamic)
	}
	for _, cmd := range []string{`cd "$d" && rm x`, "HOME=/etc; echo > ~/x"} {
		if in := lineInput(cmd, cwd, "/nonexistent"); !slices.Contains(in.Dynamic, "computed") {
			t.Errorf("%s: dynamic %q, want computed", cmd, in.Dynamic)
		}
	}
	root := lineTree(t)
	in = lineInput("rm -rf "+root+"/hom?/me", cwd, "/nonexistent")
	if got := commandPaths(t, in, "rm -rf "+root+"/hom?/me"); !slices.Contains(got, root+"/home/me") {
		t.Errorf("rm -rf …/hom?/me: paths %q, want %s among them", got, root+"/home/me")
	}
}

// After a cd the relative paths of the commands are taken from where it
// leaves the shell: after && from there, after ; from there or, if it
// failed, from where the shell was; a subshell, a pipeline and & keep
// their cd to themselves.
func TestLinePathsCd(t *testing.T) {
	root := lineTree(t)
	work, etc := root+"/work", root+"/etc"
	for _, c := range []struct {
		cmd, text string
		want      []string
	}{
		{"cd " + etc + " && cp x passwd", "cp x passwd", []string{etc + "/x", etc + "/passwd"}},
		{"cd ../etc && cp x ./passwd", "cp x ./passwd", []string{etc + "/x", etc + "/passwd"}},
		{"cd " + etc + "; cp x ./y", "cp x ./y", []string{etc + "/x", etc + "/y", work + "/y"}},
		{"cd " + etc + " || cp x ./y", "cp x ./y", []string{work + "/y"}},
		{"(cd " + etc + " && true); cp x ./y", "cp x ./y", []string{work + "/y"}},
		{"(cd " + etc + " && cp x ./y)", "cp x ./y", []string{etc + "/x", etc + "/y"}},
		{"cd " + etc + " & cp x ./y", "cp x ./y", []string{work + "/y"}},
		{"x=$(cd " + etc + "); cp x ./y", "cp x ./y", []string{work + "/y"}},
		{"cd " + etc + " | cat; cp x ./y", "cp x ./y", []string{work + "/y"}},
		{"cd " + etc + " && cd ssh", "cd ssh", []string{etc + "/ssh"}},
		{"cd " + etc + " && { cp x ./a; cd ssh; } && cp y ./b", "cp y ./b", []string{etc + "/ssh/y", etc + "/ssh/b"}},
		{"if cd " + etc + "; then cp x ./y; fi", "cp x ./y", []string{etc + "/x", etc + "/y"}},
		{"builtin cd " + etc + " && cp x ./y", "cp x ./y", []string{etc + "/x", etc + "/y"}},
		{"/usr/bin/cd " + etc + " && cp x ./y", "cp x ./y", []string{work + "/y"}},
		// The kernel takes .. from where the link leads, cd .. from its name.
		{"cd link && rm ../x", "rm ../x", []string{etc + "/x"}},
		{"cd link && cd .. && rm ./x", "rm ./x", []string{etc + "/x", work + "/x"}},
		{"cd -P link && cd .. && rm ./x", "rm ./x", []string{etc + "/x"}},
		// In the shell's own directory a bare word is a name.
		{"cd . && cp x passwd", "cp x passwd", nil},
		{"env -C " + etc + " cp x passwd", "cp x passwd", []string{etc + "/x", etc + "/passwd"}},
		{"sudo -D ssh cp x ./y", "cp x ./y", []string{work + "/ssh/x", work + "/ssh/y"}},
	} {
		in := lineInput(c.cmd, work, root+"/home/me")
		if got := commandPaths(t, in, c.text); !slices.Equal(got, sorted(c.want...)) {
			t.Errorf("%s: %s: paths %q, want %q", c.cmd, c.text, got, sorted(c.want...))
		}
		if slices.Contains(in.Dynamic, "computed") {
			t.Errorf("%s: dynamic %q", c.cmd, in.Dynamic)
		}
	}
}

// Where the line takes the shell no policy can follow, a path taken from
// the current directory is marked; an absolute one, a bare word and a line
// without either are not.
func TestLinePathsLost(t *testing.T) {
	root := lineTree(t)
	work, etc := root+"/work", root+"/etc"
	for _, c := range []struct {
		cmd      string
		computed bool
	}{
		{"for d in a b; do cd $d; done; rm ./x", true},
		{"while true; do cd ..; done; rm ./x", true},
		{"while true; do rm ./x; cd ..; done", true},
		{"while true; do rm ./x; done", false},
		{"f() { cd " + etc + "; }; f; rm ./x", true},
		{"f() { rm ./x; }", true},
		{"f() { rm " + etc + "/x; }; cd /; f", false},
		{"eval 'cd " + etc + "'; cp x ./passwd", true},
		{"alias c='cd " + etc + "'; eval c; cp x ./passwd", true},
		{"trap 'cd " + etc + "' DEBUG; cp x ./passwd", true},
		{"eval cd; cp x " + etc + "/passwd", false},
		{"bash -c 'cd " + etc + "'; cp x ./y", false},
		{"bash -c 'cd " + etc + " && cp x ./passwd'", true},
		{"cd " + etc + " && bash -c 'cp x ./passwd'", true},
		{"bash -c 'cp x ./passwd'", false},
		{"sudo -i cp x ./y", true},
		{"sudo -i cp x y", false},
		{`env -C "$d" cp x ./y`, true},
		{`cd "$d"; cd ` + etc + ` && rm ./x`, true},
		{`"$c" ` + etc + ` && rm ./x`, true},
		{"ssh host 'cd /etc && rm ./x'", false},
	} {
		in := lineInput(c.cmd, work, root+"/home/me")
		if got := slices.Contains(in.Dynamic, "computed"); got != c.computed {
			t.Errorf("%s: dynamic %q, want computed %v", c.cmd, in.Dynamic, c.computed)
		}
	}
	// After a cd to where no policy can follow, one to an absolute
	// directory is known again.
	in := lineInput(`for d in a; do cd "$d"; done; cd `+etc+` && cp x ./y`, work, root+"/home/me")
	if got, want := commandPaths(t, in, "cp x ./y"), sorted(etc+"/x", etc+"/y"); !slices.Equal(got, want) {
		t.Errorf("after a lost cd and cd %s: paths %q, want %q", etc, got, want)
	}
}

// HOME, PWD, OLDPWD and CDPATH set in the line change its paths after
// them, which the policy takes from the shell's environment: marked
// however they are set.
func TestLinePathsVars(t *testing.T) {
	root := lineTree(t)
	for _, cmd := range []string{
		"HOME=/etc; echo > ~/x",
		"HOME=/etc rm -rf ~/x",
		"export HOME=/etc",
		"export PWD=" + root + "/work/link",
		"OLDPWD=/etc; cd -",
		"CDPATH=/; cd etc && rm -rf passwd",
		"declare -x CDPATH=/",
		"local PWD=/",
		"read HOME",
		"printf -v HOME /etc",
		"unset HOME",
		"unset -v CDPATH",
		"for HOME in /etc; do rm -rf ~/x; done",
		"select PWD in /etc; do :; done",
		": ${CDPATH:=/}",
		": ${HOME=/}",
		"env HOME=/etc bash -c 'rm -rf ~/x'",
		"declare -n r=HOME",
		`x=HOME; : ${!x:=/}`,
	} {
		if in := lineInput(cmd, root+"/work", root+"/home/me"); !slices.Contains(in.Dynamic, "computed") {
			t.Errorf("%s: dynamic %q, want computed", cmd, in.Dynamic)
		}
	}
	for _, cmd := range []string{
		"export HOME",
		"echo $HOME $PWD",
		"cd ~ && ls",
		"unset -f HOME",
		": ${HOME:-/}",
		"for f in a b; do :; done",
	} {
		if in := lineInput(cmd, root+"/work", root+"/home/me"); len(in.Dynamic) != 0 {
			t.Errorf("%s: dynamic %q", cmd, in.Dynamic)
		}
	}
	// ${PATH:=…} and for PATH in … rebind as PATH=… does.
	for _, cmd := range []string{": ${PATH:=.}", "for PATH in .; do :; done"} {
		if in := lineInput(cmd, root+"/work", root+"/home/me"); !slices.Contains(in.Dynamic, "rebind") {
			t.Errorf("%s: dynamic %q, want rebind", cmd, in.Dynamic)
		}
	}
}

// chroot and sudo -R take the paths of their command from another root.
func TestLinePathsRoot(t *testing.T) {
	for _, cmd := range []string{"chroot /mnt rm -rf /etc", "sudo -R /mnt rm -rf /etc", "sudo --chroot=/mnt ls"} {
		if in := lineInput(cmd, "/", "/nonexistent"); !slices.Contains(in.Dynamic, "computed") {
			t.Errorf("%s: dynamic %q, want computed", cmd, in.Dynamic)
		}
	}
}

// A glob and braces in an operand add the paths the shell makes of it:
// the files a glob matches, where a glob matching nothing in a directory
// that is there is marked, as the line may make what it matches.
func TestLinePathsGlobs(t *testing.T) {
	root := lineTree(t)
	work, etc, me := root+"/work", root+"/etc", root+"/home/me"
	for _, c := range []struct {
		cmd, text string
		want      []string
		computed  bool
	}{
		{"cat " + etc + "/pass*", "", []string{etc + "/pass*", etc + "/passwd"}, false},
		{"cat " + etc + "/PASS?D", "", []string{etc + "/PASS?D", etc + "/passwd"}, false},
		{"rm -rf " + root + "/hom[e]/me", "", []string{root + "/hom[e]/me", me}, false},
		{"rm -rf " + root + "/*/ssh/", "", []string{root + "/*/ssh", etc + "/ssh"}, false},
		{"rm -rf ~/*.txt", "", []string{me + "/*.txt", me + "/notes.txt", me + "/todo.txt"}, false},
		{`rm -rf "$HOME"/*.txt`, "", []string{me + "/*.txt", me + "/notes.txt", me + "/todo.txt"}, false},
		{`rm -rf "` + me + `"/n*`, "", []string{me + "/n*", me + "/notes.txt"}, false},
		{"rm -rf " + root + "/{etc,home}/x", "", []string{root + "/{etc,home}/x", etc + "/x", root + "/home/x"}, false},
		{"rm ../etc/p*", "", []string{etc + "/p*", etc + "/passwd"}, false},
		{"cd " + etc + " && rm pass*", "rm pass*", []string{etc + "/passwd"}, false},
		// Quoted, a glob is a name.
		{"rm -rf '" + etc + "/pass*'", "", []string{etc + "/pass*"}, false},
		{`rm -rf ` + etc + `/pass\*`, "", []string{etc + "/pass*"}, false},
		{"rm -rf " + etc + "/nothing*", "", []string{etc + "/nothing*"}, true},
		{"rm -rf " + etc + "/*/nothing", "", []string{etc + "/*/nothing"}, true},
		{"rm -rf " + root + "/absent/*", "", []string{root + "/absent/*"}, false},
		{"rm -rf " + etc + "/**/keys", "", []string{etc + "/**/keys"}, true},
		{"rm -rf " + etc + "/@(passwd)", "", []string{etc + "/@(passwd)"}, true},
		{"rm -rf ~-/*", "", nil, true},
		// A bare glob in the shell's own directory is a name, as a bare word.
		{"rm *.bak", "", nil, false},
		// A word of other expansions no policy knows either way.
		{`rm -rf "$d"/*`, "", nil, false},
		{"scp host:/var/log/*.log .", "", []string{work + "/host:/var/log/*.log", work}, false},
	} {
		in := lineInput(c.cmd, work, me)
		text := c.text
		if text == "" {
			text = in.Commands[0][0]
			for _, a := range in.Commands[0][1:] {
				text += " " + a
			}
		}
		if got := commandPaths(t, in, text); !slices.Equal(got, sorted(c.want...)) {
			t.Errorf("%s: paths %q, want %q", c.cmd, got, sorted(c.want...))
		}
		if got := slices.Contains(in.Dynamic, "computed"); got != c.computed {
			t.Errorf("%s: dynamic %q, want computed %v", c.cmd, in.Dynamic, c.computed)
		}
	}
}

// A rule on a path holds however the line gets there.
func TestLinePathsPolicy(t *testing.T) {
	root := lineTree(t)
	etc := root + "/etc"
	e := mustLoad(t, map[string]string{"etc.cedar": permitAll + fmt.Sprintf(`@reason("passwd")
forbid(principal, action == Action::"run", resource)
when { context.paths.contains(%q) };
`, etc+"/passwd")})
	for _, c := range []struct{ cmd, want string }{
		{"cd " + etc + " && cp x passwd", Deny},
		{"cd " + etc + " && cd ssh && cp x ../passwd", Deny},
		{"env -C " + etc + " cp x passwd", Deny},
		{"cp x " + etc + "/pass?d", Deny},
		{"cp x " + root + "/{etc,tmp}/passwd", Deny},
		{"cd " + etc + " && cp x shadow", Allow},
		{"cp x passwd", Allow},
	} {
		if d := check(t, e, lineInput(c.cmd, root+"/work", root+"/home/me")); d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}

// The guard sees the trust file however the line names it: after a cd or
// env -C to its directory, by a glob.
func TestLinePathsGuard(t *testing.T) {
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
		{"cd ~/.local/share/aish && cp /tmp/x trusted.json", Deny},
		{"env -C ~/.local/share/aish cp /tmp/x trusted.json", Deny},
		{"cd ~/.local && cd share/aish && cp /tmp/x trusted.json", Deny},
		{"cp /tmp/x ~/.local/share/aish/t*", Deny},
		{"cp /tmp/x ~/.local/share/{aish,x}/trusted.json", Deny},
		{"cd /tmp && cp /tmp/x notes.json", Allow},
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
