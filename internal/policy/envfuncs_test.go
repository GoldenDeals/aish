package policy

import (
	"context"
	"slices"
	"testing"
)

// TestParseEnvFuncs checks the functions a line puts in the environment of
// a bash, BASH_FUNC_NAME%%='() { …; }', wherever it puts them: the body is
// parsed as the code of eval, and the line is rebind. A value made at run
// time is computed too, and so are the values of the wrappers' NAME=VALUE
// read for the code of their subscripts and of ${NAME@P}. has lists argv
// that must be among the commands, not those that must not; dynamic is the
// whole list of marks, and fails tells that the line has a parse error.
func TestParseEnvFuncs(t *testing.T) {
	sudoID, sudoLs := []string{"sudo", "id"}, []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		not     [][]string
		dynamic []string
		fails   bool
	}{
		// A static function: its body is code, the name another command.
		{`env 'BASH_FUNC_ls%%=() { sudo id; }' bash -c ls`, [][]string{sudoID, {"ls"}}, nil, []string{"rebind"}, false},
		{`env 'BASH_FUNC_ls()=() { sudo id; }' bash -c ls`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`sudo 'BASH_FUNC_ls%%=() { sudo id; }' bash -c ls`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`export 'BASH_FUNC_ls%%=() { sudo id; }'; bash -c ls`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`declare -x 'BASH_FUNC_ls%%=() { sudo id; }'`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`strace -E 'BASH_FUNC_ls%%=() { sudo id; }' bash -c ls`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`systemd-run -E 'BASH_FUNC_ls%%=() { sudo id; }' bash -c ls`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`firejail '--env=BASH_FUNC_ls%%=() { sudo id; }' bash -c ls`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`env 'BASH_FUNC_ls%%=() { bash -c "sudo id"; }' bash -c ls`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{"env 'BASH_FUNC_ls%%=() { cat <<E\n$(sudo id)\nE\n}' bash -c ls", [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`env 'BASH_FUNC_x/y%%=() { sudo id; }' bash -c x/y`, [][]string{sudoID}, nil, []string{"rebind"}, false},
		{`env 'BASH_FUNC_ls%%=() { :; }' bash -c ls`, [][]string{{":"}}, nil, []string{"rebind"}, false},
		// bash imports no function of a value without "() {", but the name
		// is rebind all the same.
		{`env 'BASH_FUNC_ls%%=sudo id' bash -c ls`, nil, [][]string{sudoID}, []string{"rebind"}, false},
		// One that does not parse, or is made at run time.
		{`env 'BASH_FUNC_ls%%=() { sudo id' bash -c ls`, nil, nil, []string{"computed", "rebind"}, true},
		{`env "BASH_FUNC_ls%%=$f" bash -c ls`, nil, nil, []string{"computed", "rebind"}, false},
		{`env $'BASH_FUNC_ls%%=() { \x73udo id; }' bash -c ls`, nil, nil, []string{"computed", "rebind"}, false},
		{`strace -E "BASH_FUNC_ls%%=$f" bash -c ls`, nil, nil, []string{"computed", "rebind"}, false},
		{`firejail "--env=BASH_FUNC_ls%%=$f" bash -c ls`, nil, nil, []string{"computed", "rebind"}, false},
		{`declare -x 'BASH_FUNC_ls%%+=() { sudo id; }'`, nil, nil, []string{"computed", "rebind"}, false},
		// Other names are as they were.
		{`env BASH_FUNC_ls=x BASH_FUNCS%%=y 'X=() { sudo id; }' ls`, nil, [][]string{sudoID}, nil, false},

		// NAME=VALUE made at run time of a wrapper's option: its value is
		// read as that of env NAME=VALUE is, escapes decoded or not.
		{`strace -E $'x=a[\x24(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil, false},
		{`strace -E 'x=a[\x5c$(sudo ls)]'"$y" bash -c '((x))'`, [][]string{sudoLs}, nil, nil, false},
		{`env 'x=a[\x5c$(sudo ls)]'"$y" bash -c '((x))'`, [][]string{sudoLs}, nil, nil, false},
		{`systemd-run -E $'x=a[\x24(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil, false},
		{`strace -E $'x=\x24(sudo ls)'"$y" bash -c 'echo "${x@P}"'`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`env 'x=\x5c$(sudo ls)'"$y" bash -c 'echo "${x@P}"'`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`strace -E "x=$HOME" -E "PS1=$p" bash -c 'echo "${y@P}"'`, nil, nil, []string{"computed", "prompt"}, false},
		{`strace -E "FOO=$HOME" ls`, nil, nil, nil, false},
		{`env FOO=bar ls`, nil, nil, nil, false},
	} {
		s, err := Parse(c.src, "", "")
		if (err != nil) != c.fails {
			t.Errorf("%s: error %v", c.src, err)
		}
		in := func(argv []string) bool {
			return slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, argv) })
		}
		for _, argv := range c.has {
			if !in(argv) {
				t.Errorf("%s: %q not among the commands %q", c.src, argv, s.Commands)
			}
		}
		for _, argv := range c.not {
			if in(argv) {
				t.Errorf("%s: %q among the commands %q", c.src, argv, s.Commands)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// With deny = ["sudo *"] the function a line puts in the environment of a
// bash and the code of a wrapper's NAME=VALUE are denied when they run
// sudo, asked about when made at run time, and the lines without them pass
// as before.
func TestEnvFuncsRules(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	sudo := `matches "sudo *"`
	for _, c := range []struct{ cmd, want, reason string }{
		{`env 'BASH_FUNC_ls%%=() { sudo id; }' bash -c ls`, Deny, sudo},
		{`export 'BASH_FUNC_ls%%=() { sudo id; }'; bash -c ls`, Deny, sudo},
		{`strace -E 'BASH_FUNC_ls%%=() { sudo id; }' bash -c ls`, Deny, sudo},
		{`strace -E $'x=a[\x24(sudo ls)]' bash -c '((x))'`, Deny, sudo},
		{`env 'x=a[\x5c$(sudo ls)]'"$y" bash -c '((x))'`, Deny, sudo},
		{`env "BASH_FUNC_ls%%=$f" bash -c ls`, Ask, "command built at run time (computed, rebind)"},
		{`env 'BASH_FUNC_ls%%=() { echo hi; }' bash -c ls`, Ask, "command built at run time (rebind)"},
		{`env FOO=bar ls`, Allow, ""},
		{`strace -E FOO=bar ls`, Allow, ""},
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
