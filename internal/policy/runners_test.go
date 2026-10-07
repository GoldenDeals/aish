package policy

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseRunners checks the commands that run code of theirs their words
// do not show as a command: tmux, screen, parallel and the aliases of git.
// has lists argv that must be among the commands, dynamic is the whole list
// of marks.
func TestParseRunners(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	sudo := []string{"sudo"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		// A window of tmux runs one word through the shell, more as they are.
		{`tmux new-window 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux new-session -d -s w 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux new -d sudo ls`, [][]string{sudoLs}, nil},
		{`tmux neww -n x 'cd /tmp && sudo ls'`, [][]string{{"cd", "/tmp"}, sudoLs}, nil},
		{`tmux new-w 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux splitw -h -l 20 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux split-window -t "$t" 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux respawn-pane -k 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux display-popup -E 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux -L sock -f /dev/null new-session -d 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux new-window vim "$f"`, [][]string{{"vim", "$f"}}, nil},
		{`tmux new-window "$cmd"`, nil, []string{"computed"}},
		{`tmux new-window sudo $cmd`, nil, []string{"computed"}},
		{`tmux split-window -t $t 'sudo ls'`, [][]string{sudoLs}, []string{"computed"}},
		{`tmux $opts new-window x`, nil, []string{"computed"}},
		{`tmux "$sub" 'sudo ls'`, nil, []string{"computed"}},
		{`tmux -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux new-session -d \; new-window 'sudo ls' \; attach`, [][]string{sudoLs}, nil},
		{`tmux new -d 'vim'\; neww 'sudo ls'`, [][]string{{"vim"}, sudoLs}, nil},
		{`tmux run-shell 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux run -b -d 1 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux run 'echo #{pane_id}'`, nil, []string{"computed"}},
		{`tmux run -C 'new-window x'`, nil, []string{"computed"}},
		{`tmux pipe-pane -o 'cat >> /tmp/log'`, [][]string{{"cat"}}, nil},
		{`tmux detach -E 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux if-shell 'test -f x' 'kill-server'`, [][]string{{"test", "-f", "x"}}, []string{"computed"}},
		{`tmux if -F 1 'new-window x'`, nil, []string{"computed"}},
		{`tmux display -p '#(sudo ls)'`, [][]string{sudoLs}, nil},
		{`tmux set -g status-right '#(sudo ls) %H:%M'`, [][]string{sudoLs}, nil},
		{`tmux bind-key x run-shell 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux bind -n F1 send-keys 'sudo ls' Enter`, [][]string{sudoLs}, nil},
		{`tmux confirm-before kill-server`, nil, []string{"computed"}},
		{`tmux set-hook -g after-new-window 'run x'`, nil, []string{"computed"}},
		{`tmux source-file ~/.tmux.conf`, nil, []string{"source"}},
		{`tmux paste-buffer -t w`, nil, []string{"computed"}},
		{`tmux -C attach`, nil, []string{"stdin"}},
		{`sudo tmux new-window 'rm -rf /'`, [][]string{{"rm", "-rf", "/"}}, nil},
		{`ssh box tmux new-window "'sudo ls'"`, [][]string{sudoLs}, nil},

		// send-keys types at a shell: the lines it reads are parsed.
		{`tmux send-keys 'sudo ls' Enter`, [][]string{sudoLs}, nil},
		{`tmux send-keys -t w:1 'sudo ls' C-m`, [][]string{sudoLs}, nil},
		{`tmux send -t "$pane" 'sudo ls' Enter`, [][]string{sudoLs}, nil},
		{`tmux send-keys sudo Space ls enter`, [][]string{sudoLs}, nil},
		{`tmux send-keys 'echo hi' Enter 'sudo ls' ^M`, [][]string{{"echo", "hi"}, sudoLs}, nil},
		{`tmux send-keys 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux send-keys -l 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux send-keys -H 73 75 64 6f 20 6c 73 0d`, [][]string{sudoLs}, nil},
		{`tmux send-keys 0x73 0x75 0x64 0x6f 0x20 0x6c 0x73 0xd`, [][]string{sudoLs}, nil},
		{`tmux send-keys C-c; tmux send-keys -t w q`, nil, nil},
		{`tmux send-keys 'sudp' BSpace 'o ls' Enter`, nil, []string{"computed"}},
		{`tmux send-keys 'sud' Tab ' ls' Enter`, nil, []string{"computed"}},
		{`tmux send-keys Up Enter`, nil, []string{"computed"}},
		{`tmux send-keys -K C-b c`, nil, []string{"computed"}},
		{`tmux send-keys "$cmd" Enter`, nil, []string{"computed"}},
		{`tmux send-keys -X copy-pipe-and-cancel 'sudo ls'`, [][]string{sudoLs}, nil},
		{`tmux send-keys -X cancel; tmux send-keys -X begin-selection`, nil, nil},

		// screen runs the command of its window as it is; with -X it reads
		// a command of its own anew, as in double quotes.
		{`screen -X stuff 'sudo ls\n'`, [][]string{sudoLs}, nil},
		{`screen -S w -p 0 -X stuff 'sudo ls^M'`, [][]string{sudoLs}, nil},
		{`screen -S w -X stuff "sudo ls\015"`, [][]string{sudoLs}, nil},
		{`screen -S w -X stuff 'sudo ls'`, [][]string{sudoLs}, nil},
		{`screen -r w -X stuff 'sudo ls\r'`, [][]string{sudoLs}, nil},
		{`screen -X stuff 'echo $HOME\n'`, nil, []string{"computed"}},
		{`screen -X stuff 'sud\to ls\n'`, nil, []string{"computed"}},
		{`screen -X stuff $'sudo ls\n'`, nil, []string{"computed"}},
		{`screen -X stuff "$x"`, nil, []string{"computed"}},
		{`screen -dmS w sudo ls`, [][]string{sudoLs}, nil},
		{`screen -d -m -S w -- sudo ls`, [][]string{sudoLs}, nil},
		{`screen -dm bash -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`screen -dm sleep 10`, [][]string{{"sleep", "10"}}, nil},
		{`screen -S w -X exec sudo ls`, [][]string{sudoLs}, nil},
		{`screen -S w -X exec '!..|' sudo ls`, [][]string{sudoLs}, nil},
		{`screen -X exec '!!sudo' ls`, [][]string{sudoLs}, nil},
		{`screen -X exec '!..sudo' ls`, [][]string{{"..sudo", "ls"}}, nil},
		{`screen -X screen -t x 1 sudo ls`, [][]string{sudoLs}, nil},
		{`screen -X backtick 1 0 0 sudo ls`, [][]string{sudoLs}, nil},
		{`screen -X at 0 stuff 'sudo ls\n'`, [][]string{sudoLs}, nil},
		{`screen -X bind -c x a stuff 'sudo ls\n'`, [][]string{sudoLs}, nil},
		{`screen -X eval 'stuff x'`, nil, []string{"computed"}},
		{`screen -X paste .`, nil, []string{"computed"}},
		{`screen -X source ~/.screenrc`, nil, []string{"source"}},
		{`nohup screen -dm sudo ls`, [][]string{sudoLs}, nil},

		// parallel runs its command through the shell with each value in
		// quotes; with no command each input is one.
		{`parallel 'sudo {}' ::: ls`, [][]string{{"sudo", "{}"}}, []string{"computed"}},
		{`parallel sudo ls ::: a`, [][]string{sudoLs}, nil},
		{`parallel -j4 --tag 'sudo ls {}' ::: a b`, [][]string{{"sudo", "ls", "{}"}}, nil},
		{`parallel --jobs 4 sudo ls ::: a`, [][]string{sudoLs}, nil},
		{`parallel --max-pro 4 sudo ls ::: a`, [][]string{sudoLs}, nil},
		{`parallel -kj4 sudo ls ::: a`, [][]string{sudoLs}, nil},
		{`parallel gzip ::: *.txt; find . | parallel gzip`, [][]string{{"gzip"}}, nil},
		{`parallel 'convert {} {.}.png' ::: a.jpg`, [][]string{{"convert", "{}", "{.}.png"}}, nil},
		{`parallel 'cd {} && sudo make' ::: a`, [][]string{{"sudo", "make"}}, nil},
		{`parallel ::: 'sudo ls' 'echo hi'`, [][]string{sudoLs, {"echo", "hi"}}, nil},
		{`parallel ::: sudo ::: ls`, [][]string{sudoLs}, nil},
		{`parallel <<< 'sudo ls'`, [][]string{sudoLs}, nil},
		{`find . | parallel`, nil, []string{"stdin"}},
		{`parallel -a cmds.txt; parallel :::: cmds.txt`, nil, nil},
		{`parallel {} ::: sudo`, nil, []string{"computed"}},
		{`parallel 'bash -c {}' ::: 'sudo ls'`, nil, []string{"computed"}},
		{`parallel 'echo "{}"' ::: x`, nil, []string{"computed"}},
		{`parallel "sh -c 'echo {}'" ::: x`, nil, []string{"computed"}},
		{`parallel 'echo $(basename {})' ::: x`, [][]string{{"basename", "{}"}}, nil},
		{`parallel echo "$x" ::: a`, nil, []string{"computed"}},
		{`parallel -I @@ 'echo "@@"' ::: a`, nil, []string{"computed"}},
		{`parallel -I @@ 'sudo ls @@' ::: a`, [][]string{{"sudo", "ls", "@@"}}, nil},
		{`parallel -q sudo ls ::: a`, [][]string{sudoLs}, nil},
		{`parallel -q echo '{}' ::: a`, [][]string{{"echo", "{}"}}, nil},
		{`parallel 'echo {= $_++ =}' ::: 1`, nil, []string{"computed"}},
		{`parallel --rpl '{..} s/x//' echo ::: a`, [][]string{{"echo"}}, []string{"computed"}},
		{`parallel --ssh 'sudo ssh' -S box echo ::: a`, [][]string{{"sudo", "ssh"}}, nil},
		{`parallel --pipe 'sudo wc' < f`, [][]string{{"sudo", "wc"}}, nil},
		{`sem 'sudo ls'; sem --wait`, [][]string{sudoLs}, nil},
		{`env X=1 parallel ::: 'sudo ls'`, [][]string{sudoLs}, nil},

		// An alias of git that starts with ! is a line for the shell; one
		// that starts with an option is a command of git.
		{`git -c alias.x='!sudo ls' x`, [][]string{sudoLs}, nil},
		{`git -c 'alias.x=!sudo ls' x`, [][]string{sudoLs}, nil},
		{`git -c ALIAS.X='!sudo ls' x`, [][]string{sudoLs}, nil},
		{`git -C /tmp -c alias.x='!sudo ls' x`, [][]string{sudoLs}, nil},
		{`git -c alias.x='-c alias.y=!sudo y' x`, [][]string{sudo}, nil},
		{`timeout 5 git -c alias.x='!sudo ls' x`, [][]string{sudoLs}, nil},
		{`git -c "alias.x=$v" x`, nil, []string{"computed"}},
		{`git --config-env=alias.x=CMD x`, nil, []string{"computed"}},
		{`git --config-env user.name=NAME commit; git -c user.name=me commit -m "$msg"`, nil, nil},
		{`git -C "$dir" status; git --git-dir="$d" log; git -c alias.co=checkout co`, nil, nil},
		{`git -C $dir status`, nil, []string{"computed"}},
		{`git $opts status`, nil, []string{"computed"}},
		{`git "$cmd"`, nil, []string{"computed"}},

		// git config keeps it for later: a name of a command of git will
		// run what the line does not.
		{`git config alias.x '!rm -rf ~'`, [][]string{{"rm", "-rf", "~"}}, []string{"rebind"}},
		{`git config --global alias.x '!sudo ls'`, [][]string{sudoLs}, []string{"rebind"}},
		{`git config set --global alias.x '!sudo ls'`, [][]string{sudoLs}, []string{"rebind"}},
		{`git -C repo config --local alias.st '!git status -s'`, [][]string{{"git", "status", "-s"}}, []string{"rebind"}},
		{`git config alias.x "$v"`, nil, []string{"computed", "rebind"}},
		{`git config "$k" '!sudo ls'`, nil, []string{"computed"}},
		{`git config --global alias.co checkout; git config user.email "$e"; git config --get alias.x`, nil, nil},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		for _, argv := range c.has {
			if !slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, argv) }) {
				t.Errorf("%s: %q not among the commands %q", c.src, argv, s.Commands)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// What is no code of theirs adds nothing: screen listing its sessions or
// attaching, tmux with commands of no code, parallel and git with none.
func TestParseRunnersNone(t *testing.T) {
	for _, src := range []string{
		`screen -ls`, `screen -list w`, `screen -wipe`, `screen -r w`, `screen -d w`,
		`screen -dm sleep`, `screen -X quit`, `screen -S w -X detach`,
		`tmux`, `tmux ls`, `tmux attach -t w`, `tmux kill-server`, `tmux set -g mouse on`,
		`tmux show -g`, `tmux new -d`, `tmux send-keys`, `tmux list-keys`,
		`parallel --version`, `sem --wait`,
		`git status`, `git -C /tmp log`, `git config --list`,
	} {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if len(s.Commands) != 1 || s.Dynamic != nil {
			t.Errorf("%s: commands %q, dynamic %q", src, s.Commands, s.Dynamic)
		}
	}
}

// Code a runner hands to a shell that does not parse is a parse error of
// the line, as the code of bash -c is.
func TestParseRunnersError(t *testing.T) {
	for _, src := range []string{
		`tmux new-window 'echo "'`, `tmux send-keys 'echo "' Enter`, `screen -X stuff 'echo "\n'`,
		`parallel 'echo "' ::: a`, `git -c alias.x='!echo "' x`, `git config alias.x '!echo "'`,
	} {
		in := Input{Tool: "bash"}
		in.HandOff(src)
		if in.ParseError == "" {
			t.Errorf("%s: no ParseError", src)
		}
	}
}

// The redirections of the code of a runner write files here. A pane of
// tmux, a window of screen -X, the top of a repository of git and the
// --workdir of parallel are no cwd of the line: a relative file written
// there, or anywhere in the line, is unknown, as after a cd.
func TestParseRunnersWrites(t *testing.T) {
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`tmux new-window 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`tmux send-keys 'echo x > /etc/x' Enter`, []string{"/etc/x"}, nil},
		{`screen -X stuff 'echo x >> /etc/x\n'`, []string{"/etc/x"}, nil},
		{`parallel 'echo {} > /etc/x' ::: a`, []string{"/etc/x"}, nil},
		{`git -c alias.x='!echo x > /etc/x' x`, []string{"/etc/x"}, nil},
		{`screen -dm sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`parallel 'echo {} > out' ::: a`, []string{"/w/out"}, nil},
		{`tmux send-keys 'echo x > out' Enter`, nil, []string{"computed"}},
		{`tmux new-window 'make'; echo x > out`, nil, []string{"computed"}},
		{`screen -X stuff 'echo x > out\n'`, nil, []string{"computed"}},
		{`git -c alias.x='!echo x > out' x`, nil, []string{"computed"}},
		{`parallel --wd /tmp 'echo {} > out' ::: a`, nil, []string{"computed"}},
	} {
		s, err := Parse(c.src, "/w", "/h")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Writes, c.writes) || !slices.Equal(s.Dynamic, c.dynamic) || s.UnknownWrite != (c.dynamic != nil) {
			t.Errorf("%s: writes %q, dynamic %q, unknown %v; want %q, %q", c.src, s.Writes, s.Dynamic, s.UnknownWrite, c.writes, c.dynamic)
		}
	}
}

// The rules and Cedar judge the code of a runner as any command, and the
// example policy asks about an alias git config keeps.
func TestRunnersPolicy(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	cedar := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("rm of /")
forbid(principal, action == Action::"run", resource == Command::"rm")
when { context.paths.contains("/") };
`})
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
		{rules, `tmux new-window 'sudo ls'`, Deny},
		{rules, `tmux send-keys 'sudo ls' Enter`, Deny},
		{rules, `screen -X stuff 'sudo ls\n'`, Deny},
		{rules, `parallel 'sudo {}' ::: ls`, Deny},
		{rules, `git -c alias.x='!sudo ls' x`, Deny},
		{rules, `tmux send-keys 'make test' Enter`, Allow},
		{cedar, `tmux new-window 'rm -rf /'`, Deny},
		{cedar, `git config alias.x '!rm -rf /'`, Deny},
		{cedar, `tmux new-window 'rm -rf ./x'`, Allow},
		{example, `git config alias.x '!ls'`, Ask},
		{example, `git config --global alias.co checkout`, Allow},
		{example, `tmux send-keys -t w 'make test' Enter`, Allow},
	} {
		if d := check(t, c.e, callInput("bash", map[string]any{"command": c.cmd}, home)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}
