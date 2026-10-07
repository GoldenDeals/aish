package policy

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestParseCodeVars checks the variables programs take code from: a
// command (GIT_SSH_COMMAND, PAGER, EDITOR) is parsed from a static value
// as bash -c is, wherever the line assigns it, and marked computed when
// made at run time; a variable that has programs load code (LD_PRELOAD,
// NODE_OPTIONS) is rebind. has lists argv that must be among the
// commands; dynamic is the whole list of marks.
func TestParseCodeVars(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`GIT_SSH_COMMAND='sudo ls' git fetch`, [][]string{sudoLs, {"git", "fetch"}}, nil},
		{`GIT_SSH_COMMAND='ssh -i ~/.ssh/k' git fetch`, [][]string{{"ssh", "-i", "~/.ssh/k"}}, nil},
		{`export PAGER='sudo ls'`, [][]string{sudoLs}, nil},
		{`export 'PAGER=sudo ls'`, [][]string{sudoLs}, nil},
		{`declare -x EDITOR='sudo ls'; local VISUAL=x; readonly GIT_PAGER='sudo ls'`, [][]string{sudoLs, {"x"}}, nil},
		{`builtin export MANPAGER='sudo ls'`, [][]string{sudoLs}, nil},
		{`env EDITOR='sudo ls' crontab -e`, [][]string{sudoLs, {"crontab", "-e"}}, nil},
		{`env -i SUDO_ASKPASS=/tmp/x sudo -A ls`, [][]string{{"/tmp/x"}}, nil},
		{`sudo GIT_EXTERNAL_DIFF='sudo ls' git diff`, [][]string{sudoLs}, nil},
		{`env -S GIT_EDITOR=vi git commit`, [][]string{{"vi"}, {"git", "commit"}}, nil},
		{`PAGER='sudo ls'`, [][]string{sudoLs}, nil},
		{`PAGER[0]='sudo ls'; FCEDIT=('sudo ls')`, [][]string{sudoLs}, nil},
		{`for EDITOR in vi 'sudo ls'; do crontab -e; done`, [][]string{{"vi"}, sudoLs}, nil},
		{`: ${EDITOR:='sudo ls'}; : "${PAGER=sudo ls}"`, [][]string{sudoLs}, nil},
		{`BROWSER='firefox:sudo ls %s' xdg-open x`, [][]string{{"firefox"}, {"sudo", "ls", "%s"}}, nil},
		{`LESSOPEN='||-sudo ls %s' less f`, [][]string{{"sudo", "ls", "%s"}}, nil},
		{`RSYNC_RSH='ssh -p 2' rsync a h:b; GIT_PROXY_COMMAND=p git fetch`, [][]string{{"ssh", "-p", "2"}, {"p"}}, nil},
		{`bash -c "PAGER='sudo ls' git log"`, [][]string{sudoLs}, nil},
		{`eval "export GIT_SSH='sudo ls'"`, [][]string{sudoLs}, nil},
		{`PAGER='cat > /tmp/x' git log`, [][]string{{"cat"}}, nil},

		{`PAGER="$p" git log`, [][]string{{"git", "log"}}, []string{"computed"}},
		{`GIT_SSH_COMMAND='$(sudo ls)' git fetch`, [][]string{sudoLs}, []string{"computed"}},
		{`export EDITOR=$(which vim)`, [][]string{{"which", "vim"}}, []string{"computed"}},
		{`env PAGER="$p" git log`, nil, []string{"computed"}},
		{`PAGER+=' x' git log`, nil, []string{"computed"}},
		{`builtin export PAGER="$p"`, nil, []string{"computed"}},
		{`read PAGER < f`, nil, []string{"computed"}},
		{`printf -v EDITOR %s x`, nil, []string{"computed"}},
		{`declare -n r=GIT_SSH_COMMAND`, nil, []string{"computed"}},
		{`for PAGER; do git log; done`, nil, []string{"computed"}},
		{`for PAGER in *; do git log; done`, nil, []string{"computed"}},
		{`: ${EDITOR:=$e}`, nil, []string{"computed"}},
		{`: ${!x:=v}`, nil, []string{"computed"}},

		{`LD_PRELOAD=/tmp/x.so ls`, [][]string{{"ls"}}, []string{"rebind"}},
		{`export NODE_OPTIONS=--require=/tmp/x.js`, nil, []string{"rebind"}},
		{`env LD_LIBRARY_PATH=/tmp ls`, nil, []string{"rebind"}},
		{`sudo LD_AUDIT=/tmp/a.so ls`, nil, []string{"rebind"}},
		{`PYTHONPATH="$d" python3 x.py`, nil, []string{"rebind"}},
		{`GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.sshCommand GIT_CONFIG_VALUE_0='sudo ls' git fetch`, nil, []string{"rebind"}},
		{`export GIT_CONFIG_PARAMETERS="'core.pager'='sudo ls'"`, nil, []string{"rebind"}},
		{`PERL5OPT=-Mx perl a; RUBYOPT=-rx ruby a; GIT_EXEC_PATH=/tmp git log`, nil, []string{"rebind"}},
		{`for PATH in /tmp; do ls; done`, nil, []string{"rebind"}},
		{`: ${PATH:=/tmp}`, nil, []string{"rebind"}},
		{`for PS1 in '$(sudo ls)'; do :; done`, nil, []string{"prompt"}},

		{`bind -f /tmp/rc`, nil, []string{"computed"}},
		{`bind -m vi -f ~/.inputrc`, nil, []string{"computed"}},

		{`EDITOR=vim git commit`, [][]string{{"vim"}, {"git", "commit"}}, nil},
		{`export LANG=C; LC_ALL=C sort f; for f in a b; do echo "$f"; done; : ${x:=1}`, [][]string{{"sort", "f"}}, nil},
		{`PAGER= git log; export PAGER; unset EDITOR; echo "$PAGER" "${EDITOR:-vi}"`, [][]string{{"git", "log"}}, nil},
		{`ssh box "PAGER='sudo ls' git log"`, [][]string{sudoLs}, nil},
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

// The code of a variable runs on the machine of the assignment, and is
// nested as the code of bash -c is: a fifth level is left unparsed.
func TestParseCodeVarsNested(t *testing.T) {
	s, err := Parse(`ssh box "PAGER='sudo ls' git log"`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(s.Remote, func(i int) bool { return slices.Equal(s.Commands[i], []string{"sudo", "ls"}) }) {
		t.Errorf("sudo ls of ssh is not remote: %q, remote %v", s.Commands, s.Remote)
	}
	deep := `PAGER='sudo ls' git log`
	for range maxDepth {
		deep = `PAGER=` + quote(deep) + ` git log`
	}
	if s, _ := Parse(deep, "", ""); !slices.Contains(s.Dynamic, "depth") {
		t.Errorf("%s: dynamic %q, want depth", deep, s.Dynamic)
	}
}

// quote puts s in single quotes for bash.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// With deny = ["sudo *"] the code of a variable is judged as any command:
// the line that has git run sudo is denied, a value made at run time or a
// loader asks.
func TestRulesCodeVars(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want, reason string }{
		{`GIT_SSH_COMMAND='sudo ls' git fetch`, Deny, `matches "sudo *"`},
		{`export PAGER='sudo ls'`, Deny, `matches "sudo *"`},
		{`env EDITOR='sudo ls' crontab -e`, Deny, `matches "sudo *"`},
		{`GIT_PAGER='/usr/bin/sudo ls' git log`, Deny, `matches "sudo *"`},
		{`LD_PRELOAD=/tmp/x.so ls`, Ask, "command built at run time (rebind)"},
		{`export NODE_OPTIONS=--require=/tmp/x.js`, Ask, "command built at run time (rebind)"},
		{`PAGER="$p" git log`, Ask, "command built at run time (computed)"},
		{`bind -f /tmp/rc`, Ask, "command built at run time (computed)"},
		{`EDITOR=vim git commit`, Allow, ""},
		{`export LANG=C`, Allow, ""},
	} {
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%q: %s (%s), want %s (%s)", c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
