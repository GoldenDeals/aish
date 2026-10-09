package policy

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestParseShellString checks the commands that hand a string of theirs to
// a shell: ssh on another machine, su -c, flock -c, script -c and watch
// here. The string is parsed as the code of bash -c; has lists argv that
// must be among the commands, dynamic is the whole list of marks.
func TestParseShellString(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`ssh box sudo ls`, [][]string{sudoLs}, nil},
		{`ssh -p 22 -i k box 'rm -rf /'`, [][]string{{"rm", "-rf", "/"}}, nil},
		{`ssh -o X=y box ls`, [][]string{{"ls"}}, nil},
		{`ssh -tp22 -lroot box sudo ls`, [][]string{sudoLs}, nil},
		// A config file of -F may hold a ProxyCommand: see optcode_test.go.
		{`ssh -J a,b -L 1:x:2 -E log -F cfg -P tag box sudo ls`, [][]string{sudoLs}, []string{"computed"}},
		// ssh reads options again after the host, unless -- ended them.
		{`ssh box -t sudo ls`, [][]string{sudoLs}, nil},
		{`ssh box -p 22 -- sudo ls`, [][]string{sudoLs}, nil},
		{`ssh -- box -t ls`, [][]string{{"-t", "ls"}}, nil},
		{`/usr/bin/ssh box 'cd /tmp && sudo ls'`, [][]string{{"cd", "/tmp"}, sudoLs}, nil},
		// ssh joins its words with spaces: the remote shell parses them.
		{`ssh box echo '$(sudo ls)'`, [][]string{sudoLs}, nil},
		{`ssh box bash -c "'sudo ls'"`, [][]string{sudoLs}, nil},
		{`ssh a ssh b sudo ls`, [][]string{sudoLs}, nil},
		{`sudo ssh box sudo ls`, [][]string{sudoLs}, nil},
		{`timeout 5 ssh box sudo ls`, [][]string{sudoLs}, nil},
		{`find . -exec ssh box sudo ls \;`, [][]string{sudoLs}, nil},
		// An ssh that may not run hides no shell after it.
		{`find . -name ssh -exec bash -c 'sudo ls' {} +`, [][]string{sudoLs}, nil},
		{`bash -c 'ssh box sudo ls'`, [][]string{sudoLs}, nil},
		{`ssh box "$cmd"`, nil, []string{"computed"}},
		{`ssh box sudo rm "$f"`, nil, []string{"computed"}},
		// A word built at run time may be an option or the host.
		{`ssh $opts box sudo ls`, nil, []string{"computed"}},
		{`ssh box 'eval "$x"'`, nil, []string{"computed"}},

		{`su -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`su - root -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`su root -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`su -lc 'sudo ls' root`, [][]string{sudoLs}, nil},
		{`su -c'sudo ls'`, [][]string{sudoLs}, nil},
		{`su -s /bin/sh -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`su --command='sudo ls' root`, [][]string{sudoLs}, nil},
		{`su --command 'sudo ls'`, [][]string{sudoLs}, nil},
		{`su --comm 'sudo ls'`, [][]string{sudoLs}, nil},
		{`su --session-command='sudo ls'`, [][]string{sudoLs}, nil},
		// The words after the user go to its shell.
		{`su root -- -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`sudo su -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`su -c "$x"`, nil, []string{"computed"}},
		// $x may be the user, whose shell then reads stdin.
		{`su $x`, nil, []string{"computed", "stdin"}},

		{`flock /tmp/l -c 'rm -rf /'`, [][]string{{"rm", "-rf", "/"}}, nil},
		{`flock -w 5 -E 3 /tmp/l -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`flock -n --timeout 5 /tmp/l --command 'sudo ls'`, [][]string{sudoLs}, nil},
		{`flock /tmp/l -c "$x"`, nil, []string{"computed"}},

		{`script -qc 'sudo ls' /dev/null`, [][]string{sudoLs}, nil},
		{`script /dev/null -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`script -c'sudo ls'`, [][]string{sudoLs}, nil},
		{`script --command='sudo ls' -q`, [][]string{sudoLs}, nil},
		{`script -E never -c 'sudo ls'`, [][]string{sudoLs}, nil},
		// -t takes its file only in its own word.
		{`script -t -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`script -tO -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`script -c "$x"`, nil, []string{"computed"}},

		{`watch -n 1 'sudo ls'`, [][]string{sudoLs}, nil},
		{`watch -n1 -d 'sudo ls'`, [][]string{sudoLs}, nil},
		{`watch --interval 1 'sudo ls | grep x'`, [][]string{sudoLs, {"grep", "x"}}, nil},
		{`watch -q 3 sudo ls`, [][]string{sudoLs}, nil},
		{`watch "$x"`, nil, []string{"computed"}},
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

// Without a command ssh -n or -N hands no stdin to a shell there, as ssh
// box does, and flock and watch with no string of theirs run none: nothing
// is added. su and script with none run a shell that reads stdin: see
// TestParseBareShells.
func TestParseShellStringNone(t *testing.T) {
	for _, src := range []string{
		`ssh -n box`, `ssh -N -p 22 box`, `ssh -Q cipher`, `ssh -V`,
		`flock 3`, `flock -w 5 /tmp/l`,
		`watch`, `echo ssh`, `which su flock`,
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

// A string that does not parse is a parse error of the line, as the code
// of bash -c is.
func TestParseShellStringError(t *testing.T) {
	for _, src := range []string{`ssh box 'echo "'`, `su -c 'echo "'`, `flock /tmp/l -c 'echo "'`, `script -c 'echo "'`, `watch 'echo "'`} {
		in := Input{Tool: "bash"}
		in.HandOff(src)
		if in.ParseError == "" {
			t.Errorf("%s: no ParseError", src)
		}
	}
}

// Remote names the commands of ssh, in the code they run too, and those
// only; the input of a call carries it.
func TestParseRemote(t *testing.T) {
	const src = `ssh box 'sudo ls; ssh other rm x' && su -c id`
	s, err := Parse(src, "/w", "/h")
	if err != nil {
		t.Fatal(err)
	}
	remote := map[string]bool{}
	for i, argv := range s.Commands {
		remote[strings.Join(argv, " ")] = slices.Contains(s.Remote, i)
	}
	want := map[string]bool{
		"ssh box sudo ls; ssh other rm x": false, "su -c id": false, "id": false,
		"sudo ls": true, "ls": true, "ssh other rm x": true, "rm x": true,
	}
	if !maps.Equal(remote, want) {
		t.Errorf("remote %v, want %v", remote, want)
	}
	in := Input{Tool: "bash", Cwd: "/w", Home: "/h"}
	in.HandOff(src)
	if !slices.Equal(in.Remote, s.Remote) {
		t.Errorf("input remote %v, want %v", in.Remote, s.Remote)
	}
}

// The redirections of the command of ssh write files of another machine:
// they are no writes here, and its cd moves no shell here. Those of su -c,
// flock -c, script -c and watch are.
func TestParseShellStringWrites(t *testing.T) {
	home, _ := links(t, nil)
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`ssh box 'echo x > /etc/x'`, nil, nil},
		{`ssh box 'echo x > "$f"'`, nil, nil},
		{`ssh box bash -c "'echo x > /etc/x'"`, nil, nil},
		{`ssh box 'cd /etc'; echo x > out`, []string{"/w/out"}, nil},
		{`ssh box ls > out`, []string{"/w/out"}, nil},
		{`su -c 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`flock /tmp/l -c 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`script -qc 'echo x > /etc/x' /dev/null`, []string{"/etc/x"}, nil},
		{`watch 'echo x >> /etc/x'`, []string{"/etc/x"}, nil},
		{`su -c 'cd /etc; echo x > passwd'`, nil, []string{"computed"}},
	} {
		s, err := Parse(c.src, "/w", home)
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

// The rules and Cedar judge the command of ssh as any command; its words
// are no paths of this machine, so a rule on paths does not fire on it.
func TestShellStringPolicy(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	cedar := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("sudo")
forbid(principal, action == Action::"run", resource == Command::"sudo");
@reason("rm of /")
forbid(principal, action == Action::"run", resource == Command::"rm")
when { context.paths.contains("/") };
`})
	for _, c := range []struct {
		e    *Engine
		cmd  string
		want string
	}{
		{rules, `ssh box sudo ls`, Deny},
		{rules, `ssh -p 22 box -- 'sudo ls'`, Deny},
		{rules, `su -c 'sudo ls'`, Deny},
		{rules, `flock /tmp/l -c 'sudo ls'`, Deny},
		{rules, `script -qc 'sudo ls' /dev/null`, Deny},
		{rules, `watch -n 1 'sudo ls'`, Deny},
		{rules, `ssh box ls`, Allow},
		{cedar, `ssh box sudo ls`, Deny},
		{cedar, `rm -rf /`, Deny},
		{cedar, `su -c 'rm -rf /'`, Deny},
		{cedar, `ssh box 'rm -rf /'`, Allow},
		{cedar, `ssh box ssh other rm -rf /`, Allow},
		{cedar, `ssh box ls`, Allow},
	} {
		if d := check(t, c.e, bash(c.cmd)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}

// write_outside_home asks about a file known only at run time that su -c
// writes, and not about one the command of ssh writes on the box.
func TestShellStringUnknownWrite(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: Ask})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{`su -c 'echo x > "$f"'`, Ask},
		{`ssh box 'echo x > "$f"'; $cmd`, Allow},
	} {
		if d := check(t, e, bash(c.cmd)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}

// With the example policy the sudo of ssh is denied, and so is a write
// outside $HOME by su -c; a write by the command of ssh is not one here.
func TestShellStringExample(t *testing.T) {
	ctx := context.Background()
	home, _ := links(t, nil)
	t.Setenv("HOME", home)
	e, err := Load(ctx, filepath.Join("testdata", "default"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want, reason string }{
		{`ssh box sudo ls`, Deny, "sudo is not allowed for the agent"},
		{`su -c 'echo x > /etc/x'`, Deny, "writing outside $HOME"},
		{`ssh box 'echo x > /etc/x'`, Allow, ""},
		{`ssh box uptime`, Allow, ""},
	} {
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%s: %s (%s), want %s (%s)", c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
