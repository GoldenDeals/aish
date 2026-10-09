package policy

import (
	"context"
	"slices"
	"testing"
)

// A bash takes the options of set -o from SHELLOPTS and those of shopt from
// BASHOPTS when it starts, and from -k, -o and -O: the code a line hands to
// it runs in the modes they hold. With deny = ["sudo *"] the line that has
// git run sudo there is denied, a PATH asks, and a value made at run time
// may be any mode. A value without the mode is no mark.
func TestRulesShellModesStarted(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	sudo, rebind, computed := `matches "sudo *"`, "command built at run time (rebind)", "command built at run time (computed)"
	for _, c := range []struct{ cmd, want, reason string }{
		{`env SHELLOPTS=keyword bash -c 'git fetch GIT_SSH_COMMAND="sudo ls"'`, Deny, sudo},
		{`env SHELLOPTS=braceexpand:keyword:hashall sh -c 'git fetch GIT_SSH_COMMAND="sudo ls"'`, Deny, sudo},
		{`SHELLOPTS=keyword bash -c 'ls PATH=/tmp'`, Ask, rebind},
		{`export SHELLOPTS=keyword; bash -c 'ls PATH=/tmp'`, Ask, rebind},
		{`env -S 'SHELLOPTS=keyword bash -c' 'ls PATH=/tmp'`, Ask, rebind},
		{`env SHELLOPTS=keyword PAGER='ls PATH=/tmp' man ls`, Ask, rebind},
		{`bash -c 'env SHELLOPTS=keyword bash -c "ls PATH=/tmp"'`, Ask, rebind},
		{`env SHELLOPTS="$o" bash -c ls`, Ask, computed},
		{`env BASHOPTS="$o" bash -c ls`, Ask, computed},
		{`SHELLOPTS+=:keyword bash -c ls`, Ask, computed},
		{`read SHELLOPTS; bash -c ls`, Ask, computed},
		{`env BASHOPTS=cdable_vars bash -c 'x=/; cd x && rm -rf ./etc'`, Ask, computed},
		{`bash -k -c 'git fetch GIT_SSH_COMMAND="sudo ls"'`, Deny, sudo},
		{`bash -o keyword -c 'ls PATH=/tmp'`, Ask, rebind},
		{`bash -eo keyword -c 'ls PATH=/tmp'`, Ask, rebind},
		{`bash --rcfile /dev/null -ok keyword -c 'ls PATH=/tmp'`, Ask, rebind},
		{`bash -o "$o" -c 'ls PATH=/tmp'`, Ask, rebind},
		{`bash -k <<< 'ls PATH=/tmp'`, Ask, rebind},
		{`find . -exec bash -k -c 'ls PATH=/tmp' \;`, Ask, rebind},

		// No mode: as before.
		{`SHELLOPTS=braceexpand bash -c ls`, Allow, ""},
		{`env SHELLOPTS=keywords bash -c 'ls PATH=/tmp'`, Allow, ""},
		{`env BASHOPTS=keyword bash -c 'ls PATH=/tmp'`, Allow, ""},
		{`env BASHOPTS=extglob:nullglob bash -c ls`, Allow, ""},
		{`bash +k -c 'ls PATH=/tmp'`, Allow, ""},
		{`bash +o keyword -O extglob -c 'ls PATH=/tmp'`, Allow, ""},
		{`bash -c 'ls PATH=/tmp' -k`, Allow, ""},
		{`bash -- -k`, Allow, ""},
		{`bash -c 'ls PATH=/tmp'`, Allow, ""},
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

// A shell whose options have set -k on, from ~/.bashrc or a line before,
// runs every line in it: the policy reads its commands so from the start,
// and a set -k it leaves on changes nothing the next check does not know.
// So does a bash that started with keyword in SHELLOPTS of its environment.
func TestRulesShellModesOn(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	sudo, rebind := `matches "sudo *"`, "command built at run time (rebind)"
	env := []string{"HOME=" + home}
	exported := []string{"HOME=" + home, "SHELLOPTS=braceexpand:hashall:keyword"}
	for _, c := range []struct {
		cmd          string
		env, opts    []string
		want, reason string
	}{
		{`git fetch GIT_SSH_COMMAND='sudo ls'`, env, []string{"keyword"}, Deny, sudo},
		{`ls PATH=/tmp`, env, []string{"keyword"}, Ask, rebind},
		{`set -k; ls PATH=/tmp`, env, []string{"keyword"}, Ask, rebind},
		{`f() { ls PATH=bin; }`, env, []string{"keyword"}, Ask, "command built at run time (prompt, rebind)"},
		{`eval 'ls PATH=/tmp'`, env, []string{"emacs", "keyword", "histexpand"}, Ask, rebind},
		{`ls PATH=/tmp`, exported, nil, Ask, rebind},
		{`set -k`, env, []string{"keyword"}, Allow, ""},
		{`set +k; ls PATH=/tmp`, env, []string{"keyword"}, Allow, ""},
		{`set +o keyword; git fetch GIT_SSH_COMMAND='sudo ls'`, env, []string{"keyword"}, Allow, ""},
		{`ls PATH=/tmp`, env, []string{"braceexpand", "cdable_vars"}, Allow, ""},
		{`ls PATH=/tmp`, []string{"HOME=" + home, "BASHOPTS=keyword"}, nil, Allow, ""},
		{`ls PATH=/tmp`, env, nil, Allow, ""},
		{`set -k`, env, nil, Ask, "command built at run time (computed)"},
	} {
		in := NewInput("bash", map[string]any{"command": c.cmd}, home, c.env, c.opts...)
		in.HandOff(c.cmd)
		d, err := e.Check(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%q in %q %q: %s (%s), want %s (%s)", c.cmd, c.opts, c.env, d.Action, d.Reason, c.want, c.reason)
		}
	}
}

// A shell with cdable_vars on, by its options or BASHOPTS, takes cd NAME
// with no directory NAME where the variable NAME says: the commands after
// it are lost. A directory that is there is entered as without it.
func TestLineCdableShell(t *testing.T) {
	root := lineTree(t)
	work := root + "/work"
	for _, c := range []struct {
		cmd      string
		env      []string
		opts     []string
		computed bool
	}{
		{"x=/; cd x && rm -rf ./etc", nil, []string{"cdable_vars"}, true},
		{"cd x && rm -rf ./etc", []string{"BASHOPTS=cdable_vars:extglob"}, nil, true},
		{"shopt -u cdable_vars; cd x && rm ./etc", nil, []string{"cdable_vars"}, false},
		{"cd link && rm ./keys", nil, []string{"cdable_vars"}, false},
		{"cd x && rm -rf ./etc", nil, nil, false},
		{"cd x && rm -rf ./etc", []string{"SHELLOPTS=cdable_vars"}, nil, false},
	} {
		in := NewInput("bash", map[string]any{"command": c.cmd}, work, append([]string{"HOME=" + root + "/home/me"}, c.env...), c.opts...)
		in.HandOff(c.cmd)
		if got := slices.Contains(in.Dynamic, "computed"); got != c.computed {
			t.Errorf("%s in %q %q: dynamic %q, computed %v", c.cmd, c.opts, c.env, in.Dynamic, c.computed)
		}
	}
}
