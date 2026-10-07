package policy

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseKeyword checks the commands of a line that turns set -k on:
// bash takes their NAME=VALUE words for assignments to their environment
// and runs them without those words, both of which the policy sees. A line
// that leaves set -k on is computed: the lines after it run in it. has
// lists argv that must be among the commands, not those that must not be;
// dynamic is the whole list of marks.
func TestParseKeyword(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	fetch := `git fetch GIT_SSH_COMMAND='sudo ls'; set +k`
	for _, c := range []struct {
		src      string
		has, not [][]string
		dynamic  []string
	}{
		{`set -k; git fetch GIT_SSH_COMMAND='sudo ls'`, [][]string{sudoLs, {"git", "fetch"}}, nil, []string{"computed"}},
		{`set -o keyword; man PAGER='sudo ls' ls`, [][]string{sudoLs, {"man", "ls"}}, nil, []string{"computed"}},
		{`set -k; ls PATH=/tmp`, [][]string{{"ls"}}, nil, []string{"computed", "rebind"}},
		{`set -k; ls PATH=/tmp; set +k`, [][]string{{"ls"}, {"ls", "PATH=/tmp"}}, nil, []string{"rebind"}},
		{`set -k; set +k; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`set -o keyword; set +o keyword; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`ls PATH=/tmp; set -k; set +k`, nil, [][]string{{"ls"}}, nil},
		{`set "$o"; ls`, nil, nil, []string{"computed"}},
		{`set -o "$x"; ls PATH=/tmp`, [][]string{{"ls"}}, nil, []string{"computed", "rebind"}},
		{`set -e $x; set +k`, nil, nil, []string{"computed"}},

		// Every spelling of the option, and a timeout whose command the
		// words of set -k hid.
		{`set -ek; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`set -kx; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`set -o errexit -o keyword; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`set -ok keyword; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`set -o -k; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`builtin set -k; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`command set -k; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`shopt -so keyword; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`shopt -s -o keyword; ` + fetch, [][]string{sudoLs}, nil, nil},
		{`set -k; timeout 5 A=B sudo ls; set +k`, [][]string{sudoLs, {"timeout", "5", "sudo", "ls"}}, nil, nil},
		{`set -k; ls a[0]=x 'X=1' "Y=2" W\=3 x[0-9]; set +k`, [][]string{{"ls", "X=1", "Y=2", "W=3", "x[0-9]"}}, nil, nil},

		// No set -k.
		{`set -- -k; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`set - -k; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`set x -k; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`set -e; set +k; set -o pipefail; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`shopt -s keyword; shopt -u -o keyword; shopt -su -o keyword; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},
		{`echo set -k; ls PATH=/tmp`, nil, [][]string{{"ls"}}, nil},

		// A set +k that may not run, or not last, leaves set -k on.
		{`set -k; false && set +k; ls PATH=/tmp`, [][]string{{"ls"}}, nil, []string{"computed", "rebind"}},
		{`set -k; (set +k); ls PATH=/tmp`, [][]string{{"ls"}}, nil, []string{"computed", "rebind"}},
		{`for i in 1 2; do ls PATH=/tmp; set -k; done; set +k`, [][]string{{"ls"}}, nil, []string{"computed", "rebind"}},
		{`f() { set -k; }; set +k; f; ls PATH=/tmp`, [][]string{{"ls"}}, nil, []string{"computed", "rebind"}},
		{`f() { ls PATH=/tmp; }; set -k; f; set +k`, [][]string{{"ls"}}, nil, []string{"rebind"}},
		{`set -k; set +k -- "$(ls PATH=/tmp)"`, [][]string{{"ls"}}, nil, []string{"rebind"}},
		{`set -k; set +k < <(ls PATH=/tmp)`, [][]string{{"ls"}}, nil, []string{"rebind"}},
		{`set -k; set +k; cat < <(ls PATH=/tmp)`, nil, [][]string{{"ls"}}, nil},

		// Code handed to a shell: in the mode the line turns on anywhere,
		// and turning it on itself is computed.
		{`trap 'ls PATH=/tmp' EXIT; set -k; set +k`, [][]string{{"ls"}}, nil, []string{"rebind"}},
		{`eval 'set -k'; git fetch GIT_SSH_COMMAND='sudo ls'`, nil, nil, []string{"computed"}},
		{`eval 'set -k; git fetch GIT_SSH_COMMAND="sudo ls"'`, [][]string{sudoLs}, nil, []string{"computed"}},
		{`eval 'set -k'; eval 'git fetch GIT_SSH_COMMAND="sudo ls"'`, [][]string{sudoLs}, nil, []string{"computed"}},
		{`bash -c 'set -k; ls PATH=/tmp'`, [][]string{{"ls"}}, nil, []string{"computed", "rebind"}},
		{`ssh box 'set -k; git fetch GIT_SSH_COMMAND="sudo ls"'`, [][]string{sudoLs}, nil, nil},

		// The tracker follows a cd as written.
		{`set -k; builtin X=1 cd /; set +k`, nil, nil, []string{"computed"}},
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
		for _, argv := range c.not {
			if slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, argv) }) {
				t.Errorf("%s: %q among the commands %q", c.src, argv, s.Commands)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// The code a command hands to a shell is parsed once, though the command
// is there twice: as written and as set -k makes it.
func TestParseKeywordOnce(t *testing.T) {
	s, err := Parse(`set -k; X=1 bash -c 'ls' Y=2; set +k`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, argv := range s.Commands {
		if slices.Equal(argv, []string{"ls"}) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("ls %d times among %q", n, s.Commands)
	}
}

// With deny = ["sudo *"] the words set -k makes assignments are judged as
// the assignments before a command: the line that has git run sudo is
// denied, a PATH asks.
func TestRulesKeyword(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want, reason string }{
		{`set -k; git fetch GIT_SSH_COMMAND='sudo ls'`, Deny, `matches "sudo *"`},
		{`set -o keyword; man PAGER='sudo ls' ls`, Deny, `matches "sudo *"`},
		{`set -k; timeout 5 A=B sudo ls; set +k`, Deny, `matches "sudo *"`},
		{`set -k; ls PATH=/tmp; set +k`, Ask, "command built at run time (rebind)"},
		{`set -k; ls PATH=/tmp`, Ask, "command built at run time (computed, rebind)"},
		{`set -k`, Ask, "command built at run time (computed)"},
		{`eval 'set -k'; git fetch GIT_SSH_COMMAND='sudo ls'`, Ask, "command built at run time (computed)"},
		{`set -k; set +k; ls PATH=/tmp`, Allow, ""},
		{`ls PATH=/tmp`, Allow, ""},
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

// Under shopt -s cdable_vars, cd NAME with no directory NAME goes where
// the variable NAME says: the commands after it are lost, their relative
// paths computed. A directory that is there is entered as without it.
func TestLineCdableVars(t *testing.T) {
	root := lineTree(t)
	work := filepath.Join(root, "work")
	for _, c := range []struct {
		cmd      string
		computed bool
	}{
		{"x=/; shopt -s cdable_vars; cd x && rm -rf ./etc; shopt -u cdable_vars", true},
		{"shopt -s extglob cdable_vars; cd x && rm ./etc; shopt -u cdable_vars", true},
		{"shopt -s cdable_vars; pushd x && rm ./etc; shopt -u cdable_vars", true},
		{"shopt -s cdable_vars; cd x", true},
		{`shopt -s -- "$o"; cd x && rm ./etc; shopt -u cdable_vars`, true},
		{"cd x && rm -rf ./etc", false},
		{"shopt -s cdable_vars; shopt -u cdable_vars; cd x && rm ./etc", false},
		{"shopt -s cdable_vars; cd x/ && rm ./etc; shopt -u cdable_vars", false},
		{"shopt -s cdable_vars; cd " + root + "/etc && rm ./x; shopt -u cdable_vars", false},
		{"shopt -s cdable_vars; cd link && rm ./keys; shopt -u cdable_vars", false},
		{"shopt -u cdable_vars; shopt -s -o cdable_vars; cd x && rm ./etc", false},
	} {
		in := lineInput(c.cmd, work, root+"/home/me")
		if got := slices.Contains(in.Dynamic, "computed"); got != c.computed {
			t.Errorf("%s: dynamic %q, computed %v", c.cmd, in.Dynamic, c.computed)
		}
	}
	in := lineInput("shopt -s cdable_vars; cd link && rm ./keys; shopt -u cdable_vars", work, root+"/home/me")
	if got := commandPaths(t, in, "rm ./keys"); !slices.Contains(got, root+"/etc/ssh/keys") {
		t.Errorf("cd link && rm ./keys: paths %q, want %s/etc/ssh/keys among them", got, root)
	}
}
