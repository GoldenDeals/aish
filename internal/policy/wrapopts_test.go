package policy

import (
	"context"
	"reflect"
	"slices"
	"testing"
)

// TestParseWrapperOptions checks that the value of an option of a wrapper
// is no command: want is every command of the line, the one the wrapper
// runs among them with its arguments, and dynamic the whole list of marks.
func TestParseWrapperOptions(t *testing.T) {
	sudoLs := func(wrapper ...string) [][]string {
		return [][]string{append(wrapper, "sudo", "ls"), {"sudo", "ls"}, {"ls"}}
	}
	for _, c := range []struct {
		src     string
		want    [][]string
		dynamic []string
	}{
		{`env -u X sudo ls`, sudoLs("env", "-u", "X"), nil},
		{`env --unset=X sudo ls`, sudoLs("env", "--unset=X"), nil},
		{`env --unset X sudo ls`, sudoLs("env", "--unset", "X"), nil},
		{`env -uX -C /tmp -a name sudo ls`, sudoLs("env", "-uX", "-C", "/tmp", "-a", "name"), nil},
		{`env -i -- A=1 B=2 sudo ls`, sudoLs("env", "-i", "--", "A=1", "B=2"), nil},
		{`env - A=1 sudo ls`, sudoLs("env", "-", "A=1"), nil},
		{`nice -n 5 sudo ls`, sudoLs("nice", "-n", "5"), nil},
		{`nice -n5 sudo ls`, sudoLs("nice", "-n5"), nil},
		{`nice -5 sudo ls`, sudoLs("nice", "-5"), nil},
		{`nice --adjustment 5 sudo ls`, sudoLs("nice", "--adjustment", "5"), nil},
		{`stdbuf -o L sudo ls`, sudoLs("stdbuf", "-o", "L"), nil},
		{`stdbuf -oL -e L sudo ls`, sudoLs("stdbuf", "-oL", "-e", "L"), nil},
		{`timeout -s KILL 5 sudo ls`, sudoLs("timeout", "-s", "KILL", "5"), nil},
		{`timeout --signal KILL -k 1 5s sudo ls`, sudoLs("timeout", "--signal", "KILL", "-k", "1", "5s"), nil},
		{`ionice -c 3 sudo ls`, sudoLs("ionice", "-c", "3"), nil},
		{`ionice -c2 -n 7 sudo ls`, sudoLs("ionice", "-c2", "-n", "7"), nil},
		{`xargs -n 1 -P 4 -I {} sudo ls {}`, [][]string{{"xargs", "-n", "1", "-P", "4", "-I", "{}", "sudo", "ls", "{}"}, {"sudo", "ls", "{}"}, {"ls", "{}"}}, nil},
		{`xargs -i -d '\n' sudo ls`, sudoLs("xargs", "-i", "-d", `\n`), nil},
		{`chroot --userspec u:g /srv sudo ls`, sudoLs("chroot", "--userspec", "u:g", "/srv"), nil},
		{`setsid -f sudo ls`, sudoLs("setsid", "-f"), nil},
		{`nohup -- sudo ls`, sudoLs("nohup", "--"), nil},
		{`command -p sudo ls`, sudoLs("command", "-p"), nil},
		{`/usr/bin/time -o log -f %e sudo ls`, sudoLs("/usr/bin/time", "-o", "log", "-f", "%e"), nil},
		{`watch -x -n 1 sudo ls`, sudoLs("watch", "-x", "-n", "1"), nil},
		{`flock -w 5 /tmp/l sudo ls`, sudoLs("flock", "-w", "5", "/tmp/l"), nil},
		{`doas -u root sudo ls`, sudoLs("doas", "-u", "root"), nil},
		{`runuser -u root -- sudo ls`, sudoLs("runuser", "-u", "root", "--"), nil},
		{`exec -a x bash`, [][]string{{"exec", "-a", "x", "bash"}, {"bash"}}, []string{"stdin"}},
		{`exec -la x sudo ls`, sudoLs("exec", "-la", "x"), nil},
		{`sudo -u root -g wheel ls`, [][]string{{"sudo", "-u", "root", "-g", "wheel", "ls"}, {"ls"}}, nil},
		{`sudo -C 4 -D /tmp -T 9 -p pw -h box ls`, [][]string{{"sudo", "-C", "4", "-D", "/tmp", "-T", "9", "-p", "pw", "-h", "box", "ls"}, {"ls"}}, nil},
		{`sudo --user=root --preserve-env=PATH -E ls`, [][]string{{"sudo", "--user=root", "--preserve-env=PATH", "-E", "ls"}, {"ls"}}, nil},
		// sudo reads options again after VAR=value.
		{`sudo A=1 -u root B=2 ls`, [][]string{{"sudo", "A=1", "-u", "root", "B=2", "ls"}, {"ls"}}, nil},

		// command -v and -V describe a command, sudo -l lists one, ionice
		// -p sets the class of a process: they run none.
		{`command -v bash`, [][]string{{"command", "-v", "bash"}}, nil},
		{`command -pV bash`, [][]string{{"command", "-pV", "bash"}}, nil},
		{`sudo -l rm`, [][]string{{"sudo", "-l", "rm"}}, nil},
		{`ionice -c 3 -p 42`, [][]string{{"ionice", "-c", "3", "-p", "42"}}, nil},
		{`flock -n 3`, [][]string{{"flock", "-n", "3"}}, nil},
		{`timeout 5`, [][]string{{"timeout", "5"}}, nil},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !reflect.DeepEqual(s.Commands, c.want) {
			t.Errorf("%s: commands %q, want %q", c.src, s.Commands, c.want)
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// A word a wrapper reads itself that the shell may split, glob or build at
// run time may move its command: a value of an option or a NAME=value
// stays one word in double quotes, an option made of an expansion does not
// tell which it is.
func TestParseWrapperWords(t *testing.T) {
	for _, c := range []struct {
		src     string
		dynamic []string
	}{
		{`nice -n $n sudo ls`, []string{"computed"}},
		{`nice -n "$n" sudo ls`, nil},
		{`sudo -u $u ls`, []string{"computed"}},
		{`sudo -u "$u" ls`, nil},
		{`sudo -u "$@" ls`, []string{"computed"}},
		{`sudo -u "${users[@]}" ls`, []string{"computed"}},
		{`sudo -u $(id -un) ls`, []string{"computed"}},
		{`sudo -u "$(id -un)" ls`, nil},
		{`sudo -u r* ls`, []string{"computed"}},
		{`sudo -"$o" ls`, []string{"computed"}},
		{`sudo $'-\x75' root ls`, []string{"computed"}},
		{`env A=$x sudo ls`, []string{"computed"}},
		{`env A="$x" B=$'\t' sudo ls`, nil},
		{`env "$v"=1 sudo ls`, []string{"computed"}},
		{`env -C "$d" sudo ls`, nil},
		{`timeout "$t" sudo ls`, []string{"computed"}},
		// After the option that runs no command, nothing moves one.
		{`ionice -p $(pgrep x)`, nil},
		{`ionice -c $c -p 1`, []string{"computed"}},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// The shells a wrapper runs: sudo -s and -i hand the words of the command
// to theirs for -c, each escaped to stay one word, and with no command
// they, doas -s and ssh read their commands from stdin, as bash does;
// runuser -c is su -c.
func TestParseWrapperShell(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`echo x | nice -n 5 bash`, [][]string{{"bash"}}, []string{"stdin"}},
		{`echo x | env -u X bash`, [][]string{{"bash"}}, []string{"stdin"}},
		{`echo x | ssh box`, nil, []string{"stdin"}},
		{`echo x | ssh -p 22 box -l me`, nil, []string{"stdin"}},
		{`ssh box < script.sh`, nil, []string{"stdin"}},
		{`echo x | sudo ssh box`, nil, []string{"stdin"}},
		{`ssh box <<< 'sudo ls'`, [][]string{sudoLs}, nil},
		{"ssh box <<'EOF'\nsudo ls\nEOF", [][]string{sudoLs}, nil},
		{`ssh box sudo ls`, [][]string{sudoLs}, nil},
		{`ssh -n box`, nil, nil},
		{`ssh -fN -L 8080:localhost:80 box`, nil, nil},
		{`ssh box -N`, nil, nil},
		{`ssh -W host:22 box`, nil, nil},
		{`ssh -O check box`, nil, nil},
		{`ssh -G box`, nil, nil},

		{`sudo -s sudo ls`, [][]string{sudoLs}, nil},
		{`sudo -i sudo ls`, [][]string{sudoLs}, nil},
		{`sudo -u root --login sudo -n ls`, [][]string{{"sudo", "-n", "ls"}}, nil},
		{`sudo -s 'sudo ls; id'`, [][]string{{"sudo ls; id"}}, nil},
		{`sudo -s echo "it's" '$(id)'`, [][]string{{"echo", "it's", "$(id)"}}, nil},
		{`sudo -s ls "$x"`, [][]string{{"ls", "$x"}}, nil},
		{`sudo -s "$x"`, nil, []string{"computed"}},
		{`sudo -s '$SHELL'`, nil, []string{"computed"}},
		{`echo x | sudo -s`, nil, []string{"stdin"}},
		{`sudo -i <<< 'sudo ls'`, [][]string{sudoLs}, nil},
		{`echo x | doas -s`, nil, []string{"stdin"}},

		{`runuser -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`runuser - root -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`runuser root -- -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`runuser -u root sudo ls`, [][]string{sudoLs}, nil},
		{`flock /tmp/l sudo ls`, [][]string{sudoLs}, nil},
		{`flock /tmp/l -c 'sudo ls'`, [][]string{sudoLs}, nil},
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
	// A word of the command of sudo -s is no code there, the string of
	// flock -c no program, and what command -v names does not run.
	for src, not := range map[string][]string{
		`sudo -s 'sudo ls; id'`:     {"id"},
		`sudo -s echo '$(id)'`:      {"id"},
		`flock /tmp/l -c 'sudo ls'`: {"-c", "sudo ls"},
		`command -v bash`:           {"bash"},
	} {
		s, _ := Parse(src, "", "")
		if slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, not) }) {
			t.Errorf("%s: %q among the commands %q", src, not, s.Commands)
		}
	}
	if s, _ := Parse(`ssh box <<< 'sudo ls'`, "", ""); !slices.Equal(s.Remote, []int{1, 2}) {
		t.Errorf("the here-string of ssh: remote %v of %q", s.Remote, s.Commands)
	}
}

// env -C, sudo -D, -i, su -, runuser -l and chroot run the command in
// another directory: a relative file it writes is not known, as after cd.
// The value of another option is no directory.
func TestParseWrapperChdir(t *testing.T) {
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`env -C /etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`env --chdir=/etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`env -iC/etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`sudo -D /etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`sudo --chdir /etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`sudo -i sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`su - -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`su -l root -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`runuser --login root -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`chroot /srv sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`nice env -C /etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`env -C /etc sh -c 'echo x > /tmp/x'`, []string{"/tmp/x"}, nil},
		{`env -u C sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`sudo -u D sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`sudo -s sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`sudo -s 'echo x > out'`, nil, nil},
		{`su -c 'echo x > out'`, []string{"/w/out"}, nil},
		// What ssh reads from stdin runs on the host.
		{`ssh box <<< 'echo x > /etc/x'`, nil, nil},
	} {
		s, err := Parse(c.src, "/w", "/h")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Writes, c.writes) {
			t.Errorf("%s: writes %q, want %q", c.src, s.Writes, c.writes)
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// The rules and Cedar see the sudo behind a wrapper whose option takes a
// value, and the rm in the code of sudo -s; the value of an option is no
// program for them.
func TestWrapperOptionsPolicy(t *testing.T) {
	ctx := context.Background()
	for program, cmds := range map[string][]string{
		"sudo": {
			`env -u X sudo ls`, `nice -n 5 sudo ls`, `stdbuf -o L sudo ls`, `timeout -s KILL 5 sudo ls`,
			`ionice -c 3 sudo ls`, `xargs -n 1 sudo ls`, `exec -a x sudo ls`, `flock /tmp/l sudo ls`,
			`runuser -u root sudo ls`, `doas -u root sudo ls`,
		},
		"rm": {`sudo -s rm -rf /`, `sudo -i rm -rf /`, `doas -u root rm -rf /`},
	} {
		rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{program + " *"}})
		if err != nil {
			t.Fatal(err)
		}
		cedar := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("not this")
forbid(principal, action == Action::"run", resource == Command::"` + program + `");
`})
		for _, cmd := range cmds {
			for _, e := range []*Engine{rules, cedar} {
				if d := check(t, e, bash(cmd)); d.Action != Deny {
					t.Errorf("%s: %+v, want deny", cmd, d)
				}
			}
		}
	}
	e := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("root")
forbid(principal, action == Action::"run", resource == Command::"root");
`})
	if d := check(t, e, bash(`doas -u root ls; sudo -u root -g wheel ls; runuser -u root ls`)); d.Action != Allow {
		t.Errorf("the user of sudo taken for a program: %+v", d)
	}
}
