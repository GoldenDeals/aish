package policy

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseOptionCode checks the code commands take in the values of their
// options and in words of their own syntax: find -exec, ssh -o, rsync -e,
// tar --to-command, man -P, the variables of git config, the options of
// tmux. here lists argv that must be among the commands of this machine,
// there among those of another, dynamic is the whole list of marks.
func TestParseOptionCode(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src         string
		here, there [][]string
		dynamic     []string
	}{
		// find runs the words after -exec up to ";" or "{} +", a path in
		// place of each {}.
		{`find . -exec sudo ls {} \;`, [][]string{{"sudo", "ls", "{}"}}, nil, nil},
		{`find . -name '*.go' -exec sudo ls {} +`, [][]string{{"sudo", "ls", "{}"}}, nil, nil},
		{`find . -execdir sudo ls \; -print`, [][]string{sudoLs}, nil, nil},
		{`find . -ok sudo ls \;; find . -okdir sudo ls \;`, [][]string{sudoLs}, nil, nil},
		{`find . -type f -exec echo {} + -exec sudo ls \;`, [][]string{{"echo", "{}"}, sudoLs}, nil, nil},
		{`find . -exec echo + -exec sudo ls \;`, [][]string{{"echo", "+", "-exec", "sudo", "ls"}}, nil, nil},
		{`find . -exec sh -c 'sudo ls "$1"' _ {} \;`, [][]string{{"sudo", "ls", "$1"}}, nil, nil},
		{`find . -exec bash -c 'sudo rm {}' \;`, nil, nil, []string{"computed"}},
		{`find . -exec {} \;`, nil, nil, []string{"computed"}},
		{`find . -exec $cmd {} \;`, nil, nil, []string{"computed"}},
		{`find . -exec grep "$p" {} \;`, [][]string{{"grep", "$p", "{}"}}, nil, nil},
		{`sudo find / -exec sudo ls \;`, [][]string{sudoLs}, nil, nil},
		{`ssh box find . -exec sudo ls \;`, nil, [][]string{sudoLs}, nil},
		{`find . -name -exec sudo ls \;`, nil, nil, nil},
		// A word made at run time may start a command or end one.
		{`find "$d" sudo ls \;`, nil, nil, []string{"computed"}},
		{`find . -exec echo "$x" -exec sudo ls \;`, nil, nil, []string{"computed"}},
		{`find . -exec cp "$a" "$b" "$c" \;`, nil, nil, []string{"computed"}},
		{`find . "$w" -exec -exec sudo ls \;`, nil, nil, []string{"computed"}},
		{`find $dir -name x`, nil, nil, []string{"computed"}},
		{`find . {-exec,sudo,ls,\;}`, nil, nil, []string{"computed"}},

		// ssh runs the code of -o keywords here, that of RemoteCommand
		// over there.
		{`ssh -o ProxyCommand='sudo ls' x`, [][]string{sudoLs}, nil, []string{"stdin"}},
		{`ssh -oLocalCommand='sudo ls' x ls`, [][]string{sudoLs}, [][]string{{"ls"}}, nil},
		{`ssh -o 'proxycommand sudo ls' x ls`, [][]string{sudoLs}, nil, nil},
		{`ssh -o ProxyCommand="sudo ls" -o PermitLocalCommand=yes x ls`, [][]string{sudoLs}, nil, nil},
		{`ssh x -o KnownHostsCommand='sudo ls' ls`, [][]string{sudoLs}, nil, nil},
		{`ssh -o XAuthLocation='sudo ls' -X x ls`, [][]string{sudoLs}, nil, nil},
		{`ssh -o RemoteCommand='sudo ls' x`, nil, [][]string{sudoLs}, nil},
		{`ssh -o PKCS11Provider=/tmp/x.so x ls`, nil, nil, []string{"rebind"}},
		{`ssh -F cfg x`, nil, nil, []string{"computed", "stdin"}},
		{`ssh -F cfg x ls`, nil, [][]string{{"ls"}}, []string{"computed"}},
		{`ssh -o RemoteCommand=none x`, nil, nil, []string{"stdin"}},
		{`scp -o ProxyCommand='sudo ls' a b:`, [][]string{sudoLs}, nil, nil},
		{`scp a b: -S sudo`, [][]string{{"sudo"}}, nil, nil},
		{`scp -F cfg a b:`, nil, nil, []string{"computed"}},
		{`sftp -D 'sudo ls' x`, [][]string{sudoLs}, nil, []string{"stdin"}},
		{`sftp -s '/usr/bin/sudo ls' x`, nil, [][]string{{"/usr/bin/sudo", "ls"}}, []string{"stdin"}},
		{`scp "$f" b:`, nil, nil, []string{"computed"}},
		{`scp -o "ProxyCommand=$c" a b:`, nil, nil, []string{"computed"}},

		// rsync -e is a shell here, --rsync-path the program over there.
		{`rsync -e 'sudo ls' a b:`, [][]string{sudoLs}, nil, nil},
		{`rsync -avz --rsh='sudo ls' a b:`, [][]string{sudoLs}, nil, nil},
		{`rsync a b: -aesudo`, [][]string{{"sudo"}}, nil, nil},
		{`rsync -a --rsync-path='sudo rsync' a b:`, nil, [][]string{{"sudo", "rsync"}}, nil},
		{`rsync -e "ssh -o ProxyCommand='sudo ls'" a b:`, [][]string{sudoLs}, nil, nil},
		{`rsync -av "$src" b:`, nil, nil, []string{"computed"}},
		{`rsync -e "$rsh" a b:`, nil, nil, []string{"computed"}},
		{`rsync -av "--rsh=$x" a b:`, nil, nil, []string{"computed"}},

		// tar runs its commands with sh -c.
		{`tar --to-command='sudo ls' -xf a.tar`, [][]string{sudoLs}, nil, nil},
		{`tar xf a.tar --to-command 'sudo ls'`, [][]string{sudoLs}, nil, nil},
		{`tar --to-c='sudo ls' -xf a.tar`, [][]string{sudoLs}, nil, nil},
		{`tar -I 'sudo ls' -cf a.tar x`, [][]string{sudoLs}, nil, nil},
		{`tar cIf 'sudo ls' a.tar x`, [][]string{sudoLs}, nil, nil},
		{`tar --use-compress-program='sudo ls' -cf a.tar x`, [][]string{sudoLs}, nil, nil},
		{`tar --checkpoint=1 --checkpoint-action=exec='sudo ls' -cf a.tar x`, [][]string{sudoLs}, nil, nil},
		{`tar --checkpoint --to-command='sudo ls' -xf a.tar`, [][]string{sudoLs}, nil, nil},
		{`tar -F 'sudo ls' -M -cf a.tar x`, [][]string{sudoLs}, nil, nil},
		{`tar --rmt-command='sudo ls' -xf box:a.tar`, nil, [][]string{sudoLs}, nil},
		{`tar -czf out.tgz "$d"`, nil, nil, []string{"computed"}},

		{`man -P 'sudo ls' ls`, [][]string{sudoLs}, nil, nil},
		{`man --pager='sudo ls' ls`, [][]string{sudoLs}, nil, nil},
		{`man -Hsudo ls`, [][]string{{"sudo"}}, nil, nil},
		{`man "$page"`, nil, nil, []string{"computed"}},

		// The variables of git with code, in git -c, clone -c and git
		// config, which keeps them for later.
		{`git -c core.sshCommand='sudo ls' fetch`, [][]string{sudoLs}, nil, nil},
		{`git -c core.pager='sudo ls' log`, [][]string{sudoLs}, nil, nil},
		{`git -c diff.x.textconv='sudo ls' diff`, [][]string{sudoLs}, nil, nil},
		{`git -c filter.lfs.smudge='sudo ls' checkout`, [][]string{sudoLs}, nil, nil},
		{`git -c credential.helper='!sudo ls' push`, [][]string{sudoLs}, nil, nil},
		{`git -c credential.https://x.helper='store; sudo ls' push`, [][]string{{"git", "credential-store"}, sudoLs}, nil, nil},
		{`git -c pager.log='sudo ls' log`, [][]string{sudoLs}, nil, nil},
		{`git -c pager.log=false -c core.fsmonitor=true log`, nil, nil, nil},
		{`git -c include.path=/tmp/x log`, nil, nil, []string{"computed"}},
		{`git -c core.hooksPath=/tmp/h commit`, nil, nil, []string{"computed"}},
		{`git -c protocol.ext.allow=always fetch`, nil, nil, []string{"computed"}},
		{`git config core.pager 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`git config --global core.sshCommand 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`git config --file f sequence.editor 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`git config set --global gpg.program 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`git config --add include.path /tmp/x`, nil, nil, []string{"computed", "rebind"}},
		{`git config core.pager "$p"`, nil, nil, []string{"computed", "rebind"}},
		{`git clone -c core.sshCommand='sudo ls' u`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`git clone u d --config=core.pager='sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`git clone --upload-pack='sudo ls' /tmp/r`, [][]string{sudoLs}, nil, nil},
		{`git clone --template=/tmp/t u`, nil, nil, []string{"computed"}},
		{`git clone -c "$kv" u`, nil, nil, []string{"computed"}},
		{`git --exec-path=/tmp/x status`, nil, nil, []string{"rebind"}},

		// tmux keeps a command for its new windows.
		{`tmux set -g default-command 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`tmux set-option -g default-comm 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`tmux set -g default-c 'sudo ls'`, [][]string{sudoLs}, nil, []string{"computed", "rebind"}},
		{`tmux set -g copy-command 'sudo ls'; tmux setw -g lock-command 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`tmux set -g default-shell /usr/bin/sudo`, [][]string{{"/usr/bin/sudo"}}, nil, []string{"rebind"}},
		{`tmux set -ga default-command 'sudo ls'`, [][]string{sudoLs}, nil, []string{"computed", "rebind"}},
		{`tmux set -s 'command-alias[9]' 'x=run sudo'`, nil, nil, []string{"computed", "rebind"}},
		{`tmux set -g "$o" 'sudo ls'`, nil, nil, []string{"computed"}},
		{`tmux new -d \; set -g default-command 'sudo ls'`, [][]string{sudoLs}, nil, []string{"rebind"}},
		{`tmux setenv -g PAGER 'sudo ls'`, [][]string{sudoLs}, nil, nil},
		{`tmux set-environment BASH_ENV /tmp/x`, nil, nil, []string{"prompt"}},
		{`tmux setenv -g PATH "$p"`, nil, nil, []string{"rebind"}},
		{`tmux setenv -h PAGER 'sudo ls'; tmux setenv -u PAGER`, nil, nil, nil},
		{`tmux new-window -e PAGER='sudo ls' man ls`, [][]string{sudoLs, {"man", "ls"}}, nil, nil},
		{`tmux new -d -e "PATH=$p" vim`, [][]string{{"vim"}}, nil, []string{"rebind"}},
		{`tmux -f evil.conf new -d`, nil, nil, []string{"source"}},
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

// What is no code, or not the program, adds nothing: the options of no
// code, values of the options that take one, and a word made at run time
// that may be no option or is no program's.
func TestParseOptionCodeNone(t *testing.T) {
	for _, src := range []string{
		`find . -name '*.go' -exec gofmt -l {} +`,
		`find "$d" -name "$p" -type f -exec rm {} +`,
		`find "$HOME/x" -newermt 2020-01-01 -fprintf out '%p' -print`,
		`find . -exec cp {} "$dst" \;`,
		`ssh -o StrictHostKeyChecking=no -o ProxyCommand=none -F none box ls`,
		`ssh -o RemoteCommand=none box ls`,
		`scp -P 22 -i ~/.ssh/k ./"$f" box:`,
		`rsync -avz --exclude "$p" --exclude="$q" --delete src/ box:dst/`,
		`rsync -av "$HOME/x" "${PWD}/y"`,
		`tar -czf out.tgz *.go`,
		`tar -xzf "$archive" -C "$dir"`,
		`tar --checkpoint=10 --checkpoint-action=dot -cf a.tar x`,
		`man -k printf`,
		`git config --global pager.log false; git config --get core.pager; git config --unset core.editor`,
		`git -c core.pager= log; git --exec-path`,
		`tmux set -g mouse on; tmux set -u default-command; tmux show -g default-command`,
		`tmux setenv -g FOO bar; tmux new -d -e FOO="$x" vim`,
		`echo tar "$x"; grep -l rsync "$f"; which find man`,
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

// The code of -execdir runs in the directory of each path, that of find
// -exec in that of find: a relative file it writes is unknown only for the
// first.
func TestParseOptionCodeWrites(t *testing.T) {
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`find . -exec sh -c 'echo x > out' \;`, []string{"/w/out"}, nil},
		{`find . -exec sh -c 'echo x > /etc/x' \;`, []string{"/etc/x"}, nil},
		{`find . -execdir sh -c 'echo x > out' \;`, nil, []string{"computed"}},
		{`ssh -o ProxyCommand='nc x 22 > /etc/x' box ls`, []string{"/etc/x"}, nil},
		{`ssh -o RemoteCommand='ls > /etc/x' box`, nil, nil},
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

// The rules and Cedar judge the code in options as any command.
func TestOptionCodePolicy(t *testing.T) {
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
		{rules, `ssh -o ProxyCommand='sudo ls' x`, Deny},
		{rules, `find . -exec sudo ls {} \;`, Deny},
		{rules, `rsync -e 'sudo ls' a b:`, Deny},
		{rules, `tar --to-command='sudo ls' -xf a.tar`, Deny},
		{rules, `git config core.pager 'sudo ls'; git log`, Deny},
		{rules, `git config --global core.sshCommand 'sudo ls'`, Deny},
		{rules, `tmux set -g default-command 'sudo ls'; tmux new-window`, Deny},
		{rules, `find . -name '*.go' -exec gofmt -l {} +`, Allow},
		{rules, `ssh -F cfg box ls`, Ask},
		{example, `git config --global core.editor vim`, Ask},
		{example, `rsync -avz -e 'ssh -p 2222' src/ box:dst/`, Allow},
	} {
		if d := check(t, c.e, callInput("bash", map[string]any{"command": c.cmd}, home)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}
