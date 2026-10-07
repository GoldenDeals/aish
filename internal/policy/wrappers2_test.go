package policy

import (
	"context"
	"slices"
	"testing"
)

// hasCommand tells whether argv is among the commands of s.
func hasCommand(s Script, argv []string) bool {
	return slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, argv) })
}

// The wrappers of moreWrappers run the command after their options and
// operands, read as the program reads them: the rules and Cedar see it.
func TestParseMoreWrappers(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, src := range []string{
		`pkexec sudo ls`, `pkexec --user root --keep-cwd sudo ls`, `pkexec -u root sudo ls`,
		`unshare -r sudo ls`, `unshare --mount --map-root-user -R /srv -w / sudo ls`, `unshare -l x -S 0 sudo ls`,
		`nsenter -t 1 -m -u sudo ls`, `nsenter --target 1 --mount=/proc/1/ns/mnt -S 0 sudo ls`, `nsenter -a -t1 sudo ls`,
		`setpriv --reuid=0 --regid 0 --init-groups sudo ls`, `setpriv --nnp sudo ls`,
		`taskset -c 0 sudo ls`, `taskset 0x3 sudo ls`, `taskset --cpu-list 0-2 sudo ls`,
		`chrt -o sudo ls`, `chrt 5 sudo ls`, `chrt -f 10 sudo ls`, `chrt -b 0 sudo ls`, `chrt --sched-runtime 9 -d 0 sudo ls`,
		`prlimit --nofile=10 sudo ls`, `prlimit -n10 --as=1 sudo ls`, `prlimit -o RESOURCE sudo ls`,
		`systemd-run --user -p X=y sudo ls`, `systemd-run --scope -u name --slice=s -E A=1 sudo ls`, `systemd-run -tPq --wait sudo ls`,
		`run0 sudo ls`, `run0 -u root --nice=5 -D / sudo ls`, `run0 --setenv=A=1 --property=X=y sudo ls`,
		`systemd-inhibit --what=idle --why x sudo ls`, `systemd-inhibit --who=me sudo ls`,
		`strace -f -o log sudo ls`, `strace -e trace=open -s 99 -u root sudo ls`, `strace --output=log -E A=1 sudo ls`,
		`ltrace -o log -u root sudo ls`, `ltrace -e malloc -n 2 sudo ls`,
		`valgrind --tool=memcheck -q sudo ls`, `valgrind -- sudo ls`, `valgrind - sudo ls`,
		`gdb -batch -ex run --args sudo ls`, `gdb -q -args sudo ls`, `gdb --no-escape-args sudo ls`, `gdb -batch -ex r sudo`,
		`firejail --private --net=none sudo ls`, `firejail --join=x sudo ls`, `firejail -- sudo ls`,
		`fakeroot sudo ls`, `fakeroot -u -b 3 sudo ls`, `fakeroot --unknown-is-real -- sudo ls`,
		`proxychains sudo ls`, `proxychains4 -q -f conf sudo ls`, `proxychains4 -qf sudo ls`,
		`unbuffer sudo ls`, `unbuffer -p sudo ls`, `unbuffer -ignore HUP -noecho sudo ls`,
		`numactl --cpunodebind=0 --membind 0 sudo ls`, `numactl -i all -l sudo ls`,
		`cgexec -g cpu:grp --sticky sudo ls`,
		`torsocks sudo ls`, `torsocks -u me -p pass -i sudo ls`, `torsocks --port 9050 sudo ls`,
		`catchsegv sudo ls`,
		`caffeinate -i -t 60 sudo ls`, `caffeinate -w 1 sudo ls`,
		`chpst -u root -C /tmp -n -5 sudo ls`, `chpst -/ /srv sudo ls`,
		`setuidgid root sudo ls`, `setuidgid -u sudo ls`, `envuidgid root sudo ls`, `pgrphack sudo ls`,
		`setlock -n /tmp/l sudo ls`, `softlimit -m 1000 -o 9 sudo ls`, `gosu root sudo ls`, `su-exec root sudo ls`,
		`setarch x86_64 -R sudo ls`, `setarch -R sudo ls`, `setarch i686 sudo ls`, `linux32 sudo ls`, `linux64 -B sudo ls`,
		`uclampset -m 10 -M 20 sudo ls`, `choom -n 100 -- sudo ls`,
		`nice -n 5 pkexec taskset -c 0 sudo ls`,
	} {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if !hasCommand(s, sudoLs) && !hasCommand(s, []string{"sudo"}) {
			t.Errorf("%s: sudo not among the commands %q", src, s.Commands)
		}
		if s.Dynamic != nil {
			t.Errorf("%s: dynamic %q", src, s.Dynamic)
		}
	}
	// A word a wrapper takes for a value or an operand is no program, and
	// with an option that runs nothing the operands run nothing either.
	for src, not := range map[string][]string{
		`pkexec --version`:             {"--version"},
		`pkexec --help sudo ls`:        {"sudo", "ls"},
		`taskset -p 0x3 1234`:          {"1234"},
		`chrt -p 5 1234`:               {"1234"},
		`chrt -m`:                      {"-m"},
		`prlimit --pid 1 --nofile=10`:  {"--nofile=10"},
		`strace -p 1 -o out`:           {"out"},
		`unshare -h sudo ls`:           {"sudo", "ls"},
		`systemd-inhibit --list sudo`:  {"sudo"},
		`numactl --show sudo`:          {"sudo"},
		`setpriv --dump sudo`:          {"sudo"},
		`firejail --list sudo`:         {"sudo"},
		`unbuffer -open f sudo`:        {"sudo"},
		`gdb -p 1234`:                  {"1234"},
		`gdb --version sudo`:           {"sudo"},
		`proxychains4 -fconf sudo ls`:  {"sudo", "ls"},
		`setarch x86_64 --list sudo`:   {"sudo"},
		`systemd-run -h sudo`:          {"sudo"},
		`run0 --version sudo`:          {"sudo"},
		`caffeinate -h sudo`:           {"sudo"},
		`valgrind --version sudo`:      {"sudo"},
		`choom -p 1 -n 5`:              {"5"},
		`uclampset -p 1 -m 5`:          {"5"},
		`torsocks --version sudo`:      {"sudo"},
		`systemd-run --user -p X=y ls`: {"X=y", "ls"},
	} {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if hasCommand(s, not) {
			t.Errorf("%s: commands %q", src, s.Commands)
		}
	}
	// What the wrapper reads runs: chrt's operand is the priority only as
	// a number, proxychains4 -f takes the next word whatever follows it.
	for src, want := range map[string][]string{
		`chrt -o sudo ls`:             sudoLs,
		`chrt 5 6`:                    {"6"},
		`chrt -o 5`:                   {"5"},
		`proxychains4 -fconf sudo ls`: {"ls"},
		`prlimit -n 10 sudo ls`:       {"10", "sudo", "ls"},
		`gdb sudo --args ls -l`:       {"ls", "-l"},
		`gdb -ex run sudo core`:       {"sudo", "core"},
	} {
		if s, _ := Parse(src, "", ""); !hasCommand(s, want) {
			t.Errorf("%s: %q not among the commands %q", src, want, s.Commands)
		}
	}
}

// Code a wrapper runs besides its command, in the value of an option, is
// parsed or marked; so are the variables it sets for the command.
func TestParseWrapperOptionCode(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`strace -o '|sudo ls' true`, [][]string{sudoLs}, nil},
		{`strace -o'!sudo ls' -f true`, [][]string{sudoLs}, nil},
		{`strace --output='|sudo ls' true`, [][]string{sudoLs}, nil},
		{`strace -o "|$c" true`, nil, []string{"computed"}},
		{`strace -o "$f" true`, nil, []string{"computed"}},
		{`strace -o "/tmp/$f" true`, nil, nil},
		{`strace -o ~/"$f".log true`, nil, nil},
		{`strace -o ?x true`, nil, []string{"computed"}},
		{`strace -o log true`, nil, nil},
		{`strace -E PATH=/tmp ls`, nil, []string{"rebind"}},
		{`strace -E PS1='$(id)' ls`, nil, []string{"prompt"}},
		{`strace -E "$v"=1 ls`, nil, []string{"computed"}},
		{`strace -E LANG ls`, nil, nil},
		{`strace -E PAGER='sudo ls' man ls`, [][]string{sudoLs}, nil},
		{`systemd-run -E PAGER="$p" man ls`, nil, []string{"computed"}},
		{`firejail --env=EDITOR='sudo ls' crontab -e`, [][]string{sudoLs}, nil},

		{`systemd-run -p ExecStartPre='/bin/sudo ls' true`, nil, []string{"computed"}},
		{`systemd-run --property=ExecStopPost=/bin/rm true`, nil, []string{"computed"}},
		{`systemd-run --timer-property=OnActiveSec=1 --on-active=1 -p EnvironmentFile=/tmp/e true`, nil, []string{"computed"}},
		{`systemd-run -p Environment='PATH=/tmp X=1' ls`, nil, []string{"rebind"}},
		{`systemd-run -E PATH=/tmp ls`, nil, []string{"rebind"}},
		{`systemd-run --setenv=BASH_ENV=/tmp/x bash -c ls`, [][]string{{"ls"}}, []string{"prompt"}},
		{`systemd-run -p "$p" ls`, nil, []string{"computed"}},
		{`systemd-run -p Nice=5 -p User=me ls`, nil, nil},
		{`run0 --property=ExecStartPost=/bin/id ls`, nil, []string{"computed"}},
		{`run0 --setenv=PATH=/tmp ls`, nil, []string{"rebind"}},

		// systemd-run expands $NAME in the words of its command; run0 and
		// --expand-environment=no do not.
		{`systemd-run --user '$X' ls`, nil, []string{"computed"}},
		{`systemd-run --user sh -c 'echo ${X}'`, nil, []string{"computed"}},
		{`systemd-run --user ls '$HOME'`, nil, nil},
		{`systemd-run --expand-environment=no sh -c 'echo ${X}'`, [][]string{{"echo", "${X}"}}, nil},
		{`systemd-run --expand-environment=yes sh -c 'echo ${X}'`, nil, []string{"computed"}},
		{`run0 sh -c 'echo $X'`, [][]string{{"echo", "$X"}}, nil},

		// run0 --via-shell and -i join the words for -c, as ssh does.
		{`run0 -i 'sudo ls; id'`, [][]string{sudoLs, {"id"}}, nil},
		{`run0 --via-shell sudo ls`, [][]string{sudoLs}, nil},
		{`run0 --via-shell ls "$x"`, nil, []string{"computed"}},
		{`echo id | run0 -i`, nil, []string{"stdin"}},
		{`systemd-run --user -S <<< 'sudo ls'`, [][]string{sudoLs}, nil},
		{`echo id | torsocks --shell`, nil, []string{"stdin"}},

		// fakeroot runs the value of -l through eval echo, and the line of
		// faked, with -f, -s and -i in it, through eval.
		{`fakeroot -l '$(sudo ls)' true`, [][]string{sudoLs}, nil},
		{`fakeroot --lib 'x; sudo ls' true`, [][]string{sudoLs}, nil},
		{`fakeroot -f 'sudo ls' true`, [][]string{sudoLs}, nil},
		{`fakeroot -s 'x; sudo ls' true`, [][]string{sudoLs}, nil},
		{`fakeroot -i 'x; sudo ls' true`, [][]string{sudoLs}, nil},
		{`fakeroot -s "$f" true`, nil, []string{"computed"}},
		{`fakeroot -u -b 3 true`, nil, nil},

		{`gdb --no-escape-args /bin/true '; sudo ls'`, [][]string{sudoLs}, nil},
		{`gdb --no-escape-args /bin/true "$x"`, nil, []string{"computed"}},
		{`gdb -batch -ex r -e sudo`, [][]string{{"sudo"}}, nil},
		{`gdb -batch -ex r --se="$p"`, nil, []string{"computed"}},
		{`gdb --args sudo ';' id`, [][]string{{"sudo", ";", "id"}}, nil},

		{`firejail --env=PATH=/tmp ls`, nil, []string{"rebind"}},
		{`chpst -e ./env ls`, nil, []string{"computed"}},
		{`envdir ./env ls`, nil, []string{"computed"}},

		{`sg wheel 'sudo ls'`, [][]string{sudoLs}, nil},
		{`sg wheel -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`sg - wheel -c 'sudo ls' ignored`, [][]string{sudoLs}, nil},
		{`sg wheel "$x"`, nil, []string{"computed"}},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		for _, argv := range c.has {
			if !hasCommand(s, argv) {
				t.Errorf("%s: %q not among the commands %q", c.src, argv, s.Commands)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
	// What runs no code is no command: the user of sg, a word after the
	// string of sg, the save file of fakeroot without -s.
	for src, not := range map[string][]string{
		`sg wheel 'sudo ls'`:              {"wheel"},
		`sg - wheel -c 'sudo ls' ignored`: {"ignored"},
		`fakeroot -u true`:                {"faked", "--unknown-is-real"},
		`strace -o log true`:              {"log"},
	} {
		if s, _ := Parse(src, "", ""); hasCommand(s, not) {
			t.Errorf("%s: %q among the commands %q", src, not, s.Commands)
		}
	}
}

// xargs fills its command in: a word with the replace string of -I may
// become any text, and without it what xargs reads is appended to the
// command, which makes a program or code of it behind sh -c, env, sudo.
func TestParseXargs(t *testing.T) {
	for _, c := range []struct {
		src     string
		dynamic []string
	}{
		{`echo rm | xargs -I{} {} -rf ~`, []string{"computed"}},
		{`xargs -I{} sh -c '{}'`, []string{"computed"}},
		{`xargs -I{} sh -c 'echo {}'`, []string{"computed"}},
		{`xargs -i sh -c 'echo {}'`, []string{"computed"}},
		{`xargs --replace=X sh -c 'echo X'`, []string{"computed"}},
		{`xargs -I % bash %`, []string{"computed"}},
		{`xargs -I{} sudo {} ls`, []string{"computed"}},
		{`xargs -I{} su -c '{}'`, []string{"computed"}},
		{`xargs -J % sh -c %`, []string{"computed"}},
		{`xargs -I "$r" sh -c 'x'`, []string{"computed"}},
		{`printf 'rm -rf ~' | xargs -0 sh -c`, []string{"computed"}},
		{`find . | xargs env`, []string{"computed"}},
		{`xargs sudo`, []string{"computed"}},
		{`xargs sudo -u root`, []string{"computed"}},
		{`xargs bash`, []string{"computed", "stdin"}},
		{`xargs nice -n 5`, []string{"computed"}},
		{`xargs timeout 5`, []string{"computed"}},
		{`xargs ssh box ls`, []string{"computed"}},
		{`xargs su`, []string{"computed", "stdin"}},
		{`xargs watch ls`, []string{"computed"}},
		{`xargs xargs`, []string{"computed"}},
		{`xargs sudo -s`, []string{"computed", "stdin"}},
		// -L, -l and -n but -n 1 after -I end it: xargs appends again.
		{`xargs -I{} -L 1 sh -c`, []string{"computed"}},
		{`xargs -I{} -n 2 sudo`, []string{"computed"}},
		{`xargs -I{} -n 1 sudo`, nil},

		{`xargs rm`, nil},
		{`xargs -0 rm -f`, nil},
		{`xargs -I{} rm {}`, nil},
		{`xargs -I{} mv {} {}.bak`, nil},
		{`xargs -n 1 sudo ls`, nil},
		{`xargs sh -c 'echo "$@"' sh`, nil},
		{`xargs -I{} sh -c 'echo "$1"' sh {}`, nil},
		{`xargs`, nil},
		{`xargs -I{}`, nil},
		{`xargs -I{} sudo -u {} ls`, []string{"computed"}},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q (commands %q)", c.src, s.Dynamic, c.dynamic, s.Commands)
		}
	}
}

// A shell started with no command reads its commands from stdin: su, sg,
// newgrp, script, runuser without -u, chroot DIR, and the wrappers that run
// one when they have no command, as bash with no script does.
func TestParseBareShells(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`echo id | su`, nil, []string{"stdin"}},
		{`echo id | su -`, nil, []string{"stdin"}},
		{`echo id | su - root`, nil, []string{"stdin"}},
		{`echo id | su -s /bin/zsh root`, nil, []string{"stdin"}},
		{`echo id | su root -- -x`, nil, []string{"stdin"}},
		{`echo id | sudo su`, nil, []string{"stdin"}},
		{`su <<< 'sudo ls'`, [][]string{sudoLs}, nil},
		{`echo id | runuser root`, nil, []string{"stdin"}},
		{`echo id | runuser -l root`, nil, []string{"stdin"}},
		{`echo id | chroot /srv`, nil, []string{"stdin"}},
		{`chroot --userspec u:g /srv <<< 'sudo ls'`, [][]string{sudoLs}, nil},
		{`echo id | sg wheel`, nil, []string{"stdin"}},
		{`echo id | newgrp`, nil, []string{"stdin"}},
		{`echo id | newgrp - wheel`, nil, []string{"stdin"}},
		{`echo id | script -q /dev/null`, nil, []string{"stdin"}},
		{`echo id | unshare -r`, nil, []string{"stdin"}},
		{`echo id | nsenter -t 1 -a`, nil, []string{"stdin"}},
		{`echo id | pkexec`, nil, []string{"stdin"}},
		{`echo id | run0`, nil, []string{"stdin"}},
		{`echo id | systemd-run --user -S`, nil, []string{"stdin"}},
		{`echo id | firejail`, nil, []string{"stdin"}},
		{`echo id | firejail --join=box`, nil, []string{"stdin"}},
		{`echo id | fakeroot`, nil, []string{"stdin"}},
		{`echo id | setarch x86_64`, nil, []string{"stdin"}},
		{`echo id | linux32 -R`, nil, []string{"stdin"}},

		{`su -c id`, [][]string{{"id"}}, nil},
		{`su root -c id`, [][]string{{"id"}}, nil},
		{`su root -- -c id`, [][]string{{"id"}}, nil},
		{`su root script.sh`, nil, nil},
		{`su --help`, nil, nil},
		{`runuser -u root`, nil, nil},
		{`runuser -u root id`, [][]string{{"id"}}, nil},
		{`chroot /srv id`, [][]string{{"id"}}, nil},
		{`sg wheel id`, [][]string{{"id"}}, nil},
		{`newgrp -x`, nil, nil},
		{`script -c id`, [][]string{{"id"}}, nil},
		{`script -V`, nil, nil},
		{`unshare -r id`, [][]string{{"id"}}, nil},
		{`unshare --help`, nil, nil},
		{`pkexec --version`, nil, nil},
		{`pkexec id`, [][]string{{"id"}}, nil},
		{`systemd-run --user id`, [][]string{{"id"}}, nil},
		{`systemd-run --user`, nil, nil},
		{`firejail --list`, nil, nil},
		{`fakeroot -v`, nil, nil},
		{`setarch x86_64 --list`, nil, nil},
		{`taskset -c 0`, nil, nil},
		{`strace -p 1`, nil, nil},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		for _, argv := range c.has {
			if !hasCommand(s, argv) {
				t.Errorf("%s: %q not among the commands %q", c.src, argv, s.Commands)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// pkexec, unshare -w, nsenter -r, systemd-run as a service, run0 -u and
// the like run the command in another directory: a relative file it
// writes is not known, as after cd; --keep-cwd, --scope and -d keep it.
func TestParseMoreWrappersChdir(t *testing.T) {
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`pkexec sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`pkexec --keep-cwd sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`unshare -w /etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`unshare -r sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`nsenter -t 1 -r sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`systemd-run --user sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`systemd-run --user --scope sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`systemd-run --user -d sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`systemd-run --user -d --working-directory=/etc sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`run0 sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`run0 -u me sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`run0 -D /etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`firejail --private-cwd sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`chpst -C /etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`gdb --cd=/etc --args sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`taskset -c 0 sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		// Under another root every path of the command is another file.
		{`unshare -R /srv rm /etc/x`, nil, []string{"computed"}},
		{`nsenter -t 1 --root rm /etc/x`, nil, []string{"computed"}},
		{`chpst -/ /srv rm /etc/x`, nil, []string{"computed"}},
		{`firejail --chroot=/srv rm /etc/x`, nil, []string{"computed"}},
		{`systemd-run --root-directory=/srv rm /etc/x`, nil, []string{"computed"}},
		{`unshare -r rm /etc/x`, nil, nil},
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

// The rules and Cedar see the sudo behind the new wrappers.
func TestMoreWrappersPolicy(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	cedar := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("not this")
forbid(principal, action == Action::"run", resource == Command::"sudo");
`})
	for _, cmd := range []string{
		`pkexec sudo ls`, `unshare -r sudo ls`, `systemd-run --user -p X=y sudo ls`, `strace -f -o log sudo ls`,
		`taskset -c 0 sudo ls`, `prlimit --nofile=10 sudo ls`, `run0 -i sudo ls`, `strace -o '|sudo ls' true`,
		`sg wheel 'sudo ls'`, `fakeroot -l '$(sudo ls)' true`, `chrt -o sudo ls`, `proxychains4 -qf sudo ls`,
	} {
		for _, e := range []*Engine{rules, cedar} {
			if d := check(t, e, bash(cmd)); d.Action != Deny {
				t.Errorf("%s: %+v, want deny", cmd, d)
			}
		}
	}
}
