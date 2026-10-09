package policy

import (
	"context"
	"slices"
	"testing"
)

// Code a line leaves in the user's shell for later (a trap, a function, an
// alias, bind -x, complete -C and -F) is marked prompt; that of a subshell,
// $(…), the background or a shell the line starts is not, and neither is
// a trap that only lists, resets or ignores. The code is parsed all the
// same.
func TestParseDeferred(t *testing.T) {
	prompt := []string{"prompt"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`trap 'x' DEBUG`, [][]string{{"x"}}, prompt},
		{`trap -- 'x' EXIT`, [][]string{{"x"}}, prompt},
		{`trap ' ' INT`, nil, prompt},
		{`trap "$x" EXIT`, nil, []string{"computed", "prompt"}},
		{`trap $x`, nil, []string{"computed", "prompt"}},
		{`builtin trap x DEBUG`, [][]string{{"x"}}, prompt},
		{`command trap x DEBUG`, [][]string{{"x"}}, prompt},
		{`f() { :; }`, nil, prompt},
		{`function f { :; }`, nil, prompt},
		{`ls() { command ls -l "$@"; }`, [][]string{{"command", "ls", "-l", "$@"}}, prompt},
		{`command_not_found_handle() { :; }`, nil, prompt},
		{`alias ls='ls -l'`, [][]string{{"ls", "-l"}}, prompt},
		{`alias a=b c`, [][]string{{"b"}}, prompt},
		{`alias "$a"`, nil, []string{"computed", "prompt"}},
		{`bind -x '"\C-l": x'`, [][]string{{"x"}}, prompt},
		{`complete -C x git`, [][]string{{"x"}}, prompt},
		{`complete -F _git git`, nil, prompt},
		{`complete -W '$(x)' git`, nil, []string{"computed", "prompt"}},
		{`eval 'f() { :; }'`, nil, prompt},
		{`eval "trap 'x' EXIT"`, [][]string{{"x"}}, prompt},
		{`eval 'eval "alias a=b"'`, [][]string{{"b"}}, prompt},
		{`builtin eval 'f() { :; }'`, nil, prompt},
		{`mapfile -C 'f() { :; }' a < x`, nil, prompt},
		{`{ f() { :; }; }`, nil, prompt},
		{`true && trap x EXIT`, nil, prompt},
		{`if true; then alias a=b; fi`, nil, prompt},
		{`for i in 1; do f() { :; }; done`, nil, prompt},
		{`time trap x EXIT`, nil, prompt},
		// The last command of a pipeline runs in the shell under lastpipe.
		{`ls | f() { :; }`, nil, prompt},
		{`cd /tmp && f() { :; }`, nil, prompt},

		{`( f() { :; } )`, nil, nil},
		{`(trap x EXIT; alias a=b)`, [][]string{{"x"}, {"b"}}, nil},
		{`bash -c 'trap x EXIT'`, [][]string{{"x"}}, nil},
		{`sh -c 'f() { :; }; f'`, nil, nil},
		{`bash <<< 'alias a=b'`, [][]string{{"b"}}, nil},
		{`sudo bash -c 'f() { :; }'`, nil, nil},
		{`su -c 'trap x EXIT'`, [][]string{{"x"}}, nil},
		{`ssh host 'f() { :; }'`, nil, nil},
		{`( eval 'f() { :; }' )`, nil, nil},
		{`bash -c "eval 'f() { :; }'"`, nil, nil},
		{`echo $(f() { :; }; trap x EXIT)`, [][]string{{"x"}}, nil},
		{`cat <(alias a=b)`, nil, nil},
		{`f() { :; } &`, nil, nil},
		{`trap x EXIT | cat`, nil, nil},
		{`coproc trap x EXIT`, nil, nil},
		// A program by the name of a builtin defines nothing in the shell.
		{`env alias a=b`, nil, nil},
		{`nohup trap x EXIT`, nil, nil},
		{`trap -p`, nil, nil},
		{`trap -p EXIT`, nil, nil},
		{`trap -l`, nil, nil},
		{`trap - INT`, nil, nil},
		{`trap -- - INT`, nil, nil},
		{`trap '' INT`, nil, nil},
		{`trap INT`, nil, nil},
		{`trap`, nil, nil},
		{`alias`, nil, nil},
		{`alias ls`, nil, nil},
		{`alias -p`, nil, nil},
		{`unalias ls`, nil, nil},
		{`unset -f f`, nil, nil},
		{`declare -f f`, nil, nil},
		{`export -f f`, nil, nil},
		{`bind -l`, nil, nil},
		{`bind '"\C-l": clear-screen'`, nil, nil},
		{`bind -r '\C-l'`, nil, nil},
		{`complete -p`, nil, nil},
		{`complete -r git`, nil, nil},
		{`complete -o default git`, nil, nil},
		{`compgen -C x`, [][]string{{"x"}}, nil},
		{`compgen -F f`, nil, nil},
		{`mapfile -C echo a < x`, [][]string{{"echo"}}, nil},
		{`f`, nil, nil},
	} {
		for _, cwd := range []string{"", "/w"} {
			s, err := Parse(c.src, cwd, "")
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
				t.Errorf("%s (cwd %q): dynamic %q, want %q", c.src, cwd, s.Dynamic, c.dynamic)
			}
		}
	}
}

// zsh keeps a function for later as bash does; its hooks were marked
// before, wherever they were defined.
func TestZshDeferred(t *testing.T) {
	home := t.TempDir()
	for _, c := range []struct {
		src    string
		prompt bool
	}{
		{`f() { :; }`, true},
		{`trap 'x' EXIT`, true},
		{`( f() { :; } )`, false},
		{`zsh -c 'f() { :; }'`, false},
		{`( precmd() { :; } )`, true},
	} {
		in := NewInputIn("zsh", "bash", map[string]any{"command": c.src}, home, []string{"HOME=" + home}, "interactivecomments")
		in.HandOff(c.src)
		if got := slices.Contains(in.Dynamic, "prompt"); got != c.prompt {
			t.Errorf("%s: dynamic %q, prompt %v", c.src, in.Dynamic, c.prompt)
		}
	}
}

// With a rule of [policy], code left for later asks as an assignment to
// PROMPT_COMMAND does, and is checked as the line's own: a deny among it
// stands, and so does the guard's.
func TestRulesDeferred(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	none, err := Load(ctx, t.TempDir(), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	prompt := "command built at run time (prompt)"
	for _, c := range []struct {
		cmd              string
		withRules, alone string
		reason           string
	}{
		{`trap 'aish apply-config' DEBUG`, Ask, Allow, prompt},
		{`trap 'aish model opus' DEBUG`, Ask, Allow, prompt},
		{`ls() { command ls "$@"; }`, Ask, Allow, prompt},
		{`alias ls='ls -l'`, Ask, Allow, prompt},
		{`command_not_found_handle() { :; }`, Ask, Allow, prompt},
		{`bind -x '"\C-l": clear'`, Ask, Allow, prompt},
		{`complete -C x git`, Ask, Allow, prompt},
		{`ls() { sudo ls; }`, Deny, Allow, `matches "sudo *"`},
		{`trap 'aish yolo' DEBUG`, Deny, Deny, YoloReason},
		{`f() { aish yolo; }`, Deny, Deny, YoloReason},
		{`( f() { :; } )`, Allow, Allow, ""},
		{`trap - INT`, Allow, Allow, ""},
	} {
		in := callInput("bash", map[string]any{"command": c.cmd}, home)
		for _, e := range []struct {
			engine *Engine
			want   string
		}{{rules, c.withRules}, {none, c.alone}} {
			d, err := e.engine.Check(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != e.want || e.engine == rules && d.Reason != c.reason {
				t.Errorf("%s: %s (%s), want %s (%s)", c.cmd, d.Action, d.Reason, e.want, c.reason)
			}
		}
	}
}
