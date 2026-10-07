package policy

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseCodeRunners checks more commands that run code their words do
// not show as a command: fd -x, rg --pre, the jobs of at, the commands of
// sftp, screen -X shell and setenv, :! in the commands of vim. here lists
// argv that must be among the commands of this machine, there among those
// of another, dynamic is the whole list of marks.
func TestParseCodeRunners(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src         string
		here, there [][]string
		dynamic     []string
	}{
		// fd runs the words after -x and -X up to ";", a path in place of
		// each placeholder; a value in the word is the whole command.
		{`fd -x sudo ls`, [][]string{sudoLs}, nil, nil},
		{`fd -X sudo ls`, [][]string{sudoLs}, nil, nil},
		{`fd -e go -x sudo ls {}`, [][]string{{"sudo", "ls", "{}"}}, nil, nil},
		{`fd --exec sudo ls \; -e go`, [][]string{sudoLs}, nil, nil},
		{`fd -x echo {} \; -X sudo ls`, [][]string{{"echo", "{}"}, sudoLs}, nil, nil},
		{`fd -tf --exec-batch sudo ls`, [][]string{sudoLs}, nil, nil},
		{`fd --exec=sudo x`, [][]string{{"sudo"}}, nil, nil},
		{`fd -Hxsudo`, [][]string{{"sudo"}}, nil, nil},
		{`fd -x rm -rf -- {/.}.png`, [][]string{{"rm", "-rf", "--", "{/.}.png"}}, nil, nil},
		{`fdfind -X sudo ls`, [][]string{sudoLs}, nil, nil},
		{`sudo fd -x sudo ls`, [][]string{sudoLs}, nil, nil},
		{`ssh box fd -x sudo ls`, nil, [][]string{sudoLs}, nil},
		{`fd -x {} \;`, nil, nil, []string{"computed"}},
		{`fd -x $cmd`, nil, nil, []string{"computed"}},
		{`fd --exec="$c"`, nil, nil, []string{"computed"}},
		{`fd "$pat"`, nil, nil, []string{"computed"}},
		// A word made at run time may be the ";" that ends the command.
		{`fd -x echo "$p" -x sudo ls`, nil, nil, []string{"computed"}},

		// rg runs the program of --pre on each file, as it is, with its
		// path; a shell runs the file.
		{`rg --pre 'sudo ls' x`, [][]string{{"sudo ls", "{}"}}, nil, nil},
		{`rg foo . --pre ./conv`, [][]string{{"./conv", "{}"}}, nil, nil},
		{`rg --pre=sudo foo`, [][]string{{"sudo", "{}"}}, nil, []string{"computed"}},
		{`rg --pre sh foo`, nil, nil, []string{"computed"}},
		{`rg -i --hostname-bin sudo foo`, [][]string{{"sudo"}}, nil, nil},
		{`rg --pre "$x" foo`, nil, nil, []string{"computed"}},
		{`rg "$p" .`, nil, nil, []string{"computed"}},

		// at and batch read the commands of a job from stdin, or from the
		// file of -f.
		{`at now <<< 'sudo ls'`, [][]string{sudoLs}, nil, nil},
		{"at now + 1 hour <<EOF\nsudo ls\nEOF", [][]string{sudoLs}, nil, nil},
		{`batch <<< 'sudo ls'`, [][]string{sudoLs}, nil, nil},
		{`sudo at now <<< 'sudo ls'`, [][]string{sudoLs}, nil, nil},
		{`at now`, nil, nil, []string{"stdin"}},
		{`echo 'sudo ls' | at now`, nil, nil, []string{"stdin"}},
		{`at -f /tmp/f now`, nil, nil, []string{"source"}},
		{`at now -f /tmp/f`, nil, nil, []string{"source"}},
		{`at -f "$f" now`, nil, nil, []string{"computed"}},
		{`at "$t" <<< 'ls'`, nil, nil, []string{"computed"}},

		// sftp hands a line of "!" to a shell here, and lls with ls.
		{`sftp -b - host <<< '!sudo ls'`, [][]string{sudoLs}, nil, nil},
		{`sftp host <<< '!sudo ls'`, [][]string{sudoLs}, nil, nil},
		{`sftp host <<< ' -@ ! sudo ls'`, [][]string{sudoLs}, nil, nil},
		{"sftp host <<EOF\nget x\nlls ;sudo ls\nEOF", [][]string{{"ls"}, sudoLs}, nil, nil},
		{`sftp -b /tmp/f host`, nil, nil, []string{"source"}},
		{`sftp -b "$f" host`, nil, nil, []string{"computed"}},
		{`sftp host`, nil, nil, []string{"stdin"}},
		{`echo '!sudo ls' | sftp host`, nil, nil, []string{"stdin"}},
		{`sftp host <<< '!'`, nil, nil, []string{"stdin"}},

		// screen -X keeps a shell, a blanker and variables for new windows.
		{`screen -X shell /tmp/x`, [][]string{{"/tmp/x"}}, nil, []string{"rebind"}},
		{`screen -S w -X defshell -/usr/bin/sudo`, [][]string{{"/usr/bin/sudo"}}, nil, []string{"rebind"}},
		{`screen -X shell "$s"`, nil, nil, []string{"computed", "rebind"}},
		{`screen -X blankerprg sudo ls`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`screen -X setenv BASH_ENV /tmp/x`, nil, nil, []string{"prompt"}},
		{`screen -X setenv PAGER 'sudo ls'`, [][]string{sudoLs}, nil, nil},
		{`screen -X setenv PATH /tmp`, nil, nil, []string{"rebind"}},
		{`screen -X setenv PAGER '$X'`, nil, nil, []string{"computed"}},
		{`screen -X setenv "$n" x`, nil, nil, []string{"computed"}},
		{`screen -c /tmp/rc -dm sleep 1`, [][]string{{"sleep", "1"}}, nil, []string{"source"}},
		{`screen -c/tmp/rc -S w -X quit`, nil, nil, []string{"source"}},

		// vim runs a shell for :!, a filter and system().
		{`vim -c '!sudo ls' -c q`, nil, nil, []string{"computed"}},
		{`vim -es -c 'call system("x")' -c q f`, nil, nil, []string{"computed"}},
		{`nvim --headless +'r !ls' +q`, nil, nil, []string{"computed"}},
		{`ex -s -c '%!sort' -c wq f`, nil, nil, []string{"computed"}},
		{`vim --cmd 'let x = systemlist("ls")' f`, nil, nil, []string{"computed"}},
		{`view --remote-send ':!ls<CR>'`, nil, nil, []string{"computed"}},
		{`vim -c "$c" f`, nil, nil, []string{"computed"}},
		{`vim "$f"`, nil, nil, []string{"computed"}},
		{`vim -T -- +'!sudo ls'`, nil, nil, []string{"computed"}},

		// A value that may split puts its other words after it, where they
		// may be options; a "--" that is a value ends none.
		{`rsync --exclude $p a b:`, nil, nil, []string{"computed"}},
		{`rsync --exclude=$p a b:`, nil, nil, []string{"computed"}},
		{`tar -czf $out x`, nil, nil, []string{"computed"}},
		{`scp -P $port a b:`, nil, nil, []string{"computed"}},
		{`man -L $lang ls`, nil, nil, []string{"computed"}},
		{`rg -g $glob foo`, nil, nil, []string{"computed"}},
		{`fd -e $ext`, nil, nil, []string{"computed"}},
		{`fd --exclude=$p`, nil, nil, []string{"computed"}},
		{`rsync --exclude -- "$x" a b:`, nil, nil, []string{"computed"}},
		{`rg -e -- "$x"`, nil, nil, []string{"computed"}},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		for _, argv := range c.here {
			if !slices.ContainsFunc(indexes(0, len(s.Commands)), func(i int) bool {
				return slices.Equal(s.Commands[i], argv) && !slices.Contains(s.Remote, i)
			}) {
				t.Errorf("%s: %q not among the commands here %q (remote %v)", c.src, argv, s.Commands, s.Remote)
			}
		}
		for _, argv := range c.there {
			if !slices.ContainsFunc(s.Remote, func(i int) bool { return slices.Equal(s.Commands[i], argv) }) {
				t.Errorf("%s: %q not among the commands there %q (remote %v)", c.src, argv, s.Commands, s.Remote)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// What runs no code of the line adds nothing: fd and rg with no command,
// at listing jobs, sftp with no shell in its commands, vim with no :!.
func TestParseCodeRunnersNone(t *testing.T) {
	for _, src := range []string{
		`fd -e go`, `fd -e go -x gofmt -l`, `fd -tf -X wc -l`, `fd -x grep "$p" {}`, `fd -e "$ext" foo`,
		`fd --exclude="$p" -d 2`, `fd --exec= x`, `fd -- -x sudo`,
		`rg foo`, `rg -e "$p" .`, `rg -- "$p" .`, `rg --pre= foo`, `rg -g '*.go' -t go foo src`,
		`at -l`, `at -r 3`, `at -c 3`, `at -V`, `echo at now`,
		`sftp -b /dev/null host`, `sftp host <<< 'get x'`,
		`screen -X setenv FOO bar`, `screen -X setenv FOO "$v"`, `screen -X blankerprg`,
		`screen -c /dev/null -dm sleep 1`,
		`vim -c wq f`, `vim -c '%s/a/b/g' -c x f`, `vim ./"$f"`, `vim *.go`, `vim -- +'!x'`, `view f`, `nvim +10 f`,
		`vim -es -- "$f"`,
		// A value in quotes is one word; so is a glob, names of files.
		`rsync --exclude "$p" a b:`, `rsync --exclude="$p" a b:`, `tar -czf "$out" x`, `rg -g "$glob" foo`,
		`fd -e "$ext" -E "$p"`, `rsync --exclude *.o a b:`, `rsync -- "$x" b:`, `rg -- "$x"`,
	} {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if s.Dynamic != nil {
			t.Errorf("%s: commands %q, dynamic %q", src, s.Commands, s.Dynamic)
		}
	}
}

// The command of fd -x runs in the directory of -C: a relative file it
// writes is unknown there.
func TestParseCodeRunnersWrites(t *testing.T) {
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`fd -x sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`fd -C /tmp -x sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`at now <<< 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`sftp host <<< '!echo x > /etc/x'`, []string{"/etc/x"}, nil},
	} {
		s, err := Parse(c.src, "/w", "/h")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Writes, c.writes) || !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: writes %q, dynamic %q; want %q, %q", c.src, s.Writes, s.Dynamic, c.writes, c.dynamic)
		}
	}
}

// The rules judge the code of these runners as any command, and a mark
// asks.
func TestCodeRunnersPolicy(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := links(t, nil)
	t.Setenv("HOME", home)
	example, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		e    *Engine
		cmd  string
		want string
	}{
		{rules, `fd -x sudo ls`, Deny},
		{rules, `fd -X sudo ls`, Deny},
		{rules, `rg --pre 'sudo ls' x`, Deny},
		{rules, `at now <<< 'sudo ls'`, Deny},
		{rules, `sftp host <<< '!sudo ls'`, Deny},
		{rules, `screen -X setenv PAGER 'sudo ls'`, Deny},
		{rules, `at -f /tmp/f now`, Ask},
		{rules, `sftp -b /tmp/f host`, Ask},
		{rules, `screen -X shell /tmp/x`, Ask},
		{rules, `screen -X setenv BASH_ENV /tmp/x`, Ask},
		{rules, `vim -c '!sudo ls' -c q`, Ask},
		{rules, `fd -e go`, Allow},
		{rules, `rg foo`, Allow},
		{rules, `at -l`, Allow},
		{example, `fd -e go -x gofmt -l`, Allow},
		{example, `rg -n foo src`, Allow},
	} {
		if d := check(t, c.e, callInput("bash", map[string]any{"command": c.cmd}, home)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}
