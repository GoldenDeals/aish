package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseSetters checks the builtins that set what the shell runs later:
// their code is parsed as trap's is, and what makes a name run something
// else is marked. has lists argv that must be among the commands; dynamic
// is the whole list of marks.
func TestParseSetters(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`bind -x '"\C-a": sudo ls'`, [][]string{sudoLs}, []string{"prompt"}},
		{`bind -x '"\C-a": "sudo ls"'`, [][]string{sudoLs}, []string{"prompt"}},
		{`bind -m vi -x'"\C-a":sudo ls'`, [][]string{sudoLs}, []string{"prompt"}},
		{`builtin bind -x '"\e:": sudo ls'`, [][]string{sudoLs}, []string{"prompt"}},
		// Bash 5.3 ends the key sequence at a blank, 5.2 at a colon.
		{`bind -x '"\C-a" sudo ls #:x'`, [][]string{sudoLs, {"x"}}, []string{"prompt"}},
		{`bind -x "\"\C-a\": $cmd"`, nil, []string{"computed", "prompt"}},
		{`bind -x 'C-a: sudo ls'`, nil, []string{"computed", "prompt"}},
		{`bind '"\C-j": "sudo ls\n"'`, nil, []string{"computed", "prompt"}},
		{`bind 'C-j: "sudo ls\n"'`, nil, []string{"computed", "prompt"}},
		{`bind '"\e[A": history-search-backward'; bind 'set bell-style none'; bind -p; bind -X`, nil, nil},

		{`complete -C 'sudo ls' x`, [][]string{sudoLs}, []string{"prompt"}},
		{`compgen -C 'sudo ls' x`, [][]string{sudoLs}, nil},
		{`complete -o default -C'sudo ls' -- x`, [][]string{sudoLs}, []string{"prompt"}},
		{`complete -C "$c" x`, nil, []string{"computed", "prompt"}},
		{`compgen -W '$(sudo ls)' x`, nil, []string{"computed"}},
		{"complete -W '`sudo ls`' x", nil, []string{"computed", "prompt"}},
		{`compgen -V PS1 -W x`, nil, []string{"prompt"}},
		{`complete -F _x -W 'a b' x; compgen -c; complete -r x`, nil, []string{"prompt"}},

		{`mapfile -C 'sudo ls' -c 1 a < f`, [][]string{sudoLs}, nil},
		{`readarray -t -C 'sudo ls' a < f`, [][]string{sudoLs}, nil},
		{`mapfile -C "$cb" a < f`, nil, []string{"computed"}},
		{`readarray -t PROMPT_COMMAND < f`, nil, []string{"prompt"}},
		{`mapfile -t lines < f; readarray a < <(ls)`, [][]string{{"ls"}}, nil},

		{`hash -p /bin/rm ls`, nil, []string{"rebind"}},
		{`hash -p/bin/rm ls`, nil, []string{"rebind"}},
		{`hash "$x" ls`, nil, []string{"rebind"}},
		{`hash; hash -r; hash ls; hash -t ls; hash -d ls`, nil, nil},

		{`enable -f ./x.so x`, nil, []string{"rebind"}},
		{`enable -f ./x.so`, nil, []string{"rebind"}},
		{`enable echo`, nil, []string{"rebind"}},
		{`enable $x`, nil, []string{"rebind"}},
		{`enable -n echo; enable -d x; enable; enable -p; enable -a; enable -p echo`, nil, nil},

		{`read PS1`, nil, []string{"prompt"}},
		{`read -r -p 'x: ' PROMPT_COMMAND < f`, nil, []string{"prompt"}},
		{`read -ra PS0 <<< x`, nil, []string{"prompt"}},
		{`IFS= read -r -d '' PATH < f`, nil, []string{"rebind"}},
		{`read "$v" < f`, nil, []string{"computed"}},
		{`read 'a[$(sudo ls)]' < f`, nil, []string{"computed"}},
		{`while IFS= read -r line; do echo "$line"; done < f`, nil, nil},

		{`printf -v PS1 '%s' x`, nil, []string{"prompt"}},
		{`printf -vPATH /tmp`, nil, []string{"rebind"}},
		{`builtin printf -v 'BASH_CMDS[ls]' /bin/rm`, nil, []string{"rebind"}},
		{`printf -v "$n" x`, nil, []string{"computed"}},
		{`printf "$fmt" PS1 x`, nil, []string{"computed"}},
		{`printf $'\x2dvPS1' x`, nil, []string{"computed"}},
		{`printf "Total: $n\n"; printf "\n$x"; printf '%s\n' "$@"; printf -v x '%s' "$y"; printf -- "$x"`, nil, nil},

		{`export PATH=/tmp:$PATH`, nil, []string{"rebind"}},
		{`PATH=/tmp ls`, nil, []string{"rebind"}},
		{`PATH+=:/tmp`, nil, []string{"rebind"}},
		{`local PATH=/tmp`, nil, []string{"rebind"}},
		{`readonly 'PATH=/tmp'`, nil, []string{"rebind"}},
		{`declare -n r=PATH`, nil, []string{"rebind"}},
		{`declare -n 'r=PS1'`, nil, []string{"prompt"}},
		{`declare -n r="$x"`, nil, []string{"computed"}},
		{`declare 'a[$(sudo ls)]=1'`, nil, []string{"computed"}},
		{`env PATH=/tmp ls`, nil, []string{"rebind"}},
		{`sudo PATH=/tmp ls`, nil, []string{"rebind"}},
		{`BASH_CMDS[ls]=/bin/rm`, nil, []string{"rebind"}},
		{`BASH_ALIASES[ll]='sudo ls'`, nil, []string{"rebind"}},
		{`declare -A BASH_ALIASES=([ll]='sudo ls')`, nil, []string{"rebind"}},
		{`EXECIGNORE=/usr/bin/ls`, nil, []string{"rebind"}},
		{`MAILPATH='/tmp/m?$(sudo ls)'`, nil, []string{"prompt"}},
		{`getopts a PATH`, nil, []string{"rebind"}},
		{`getopts -- a PS1 x`, nil, []string{"prompt"}},
		{`wait -n -p PATH`, nil, []string{"rebind"}},
		{`while getopts ab: opt; do :; done; wait "$pid"; wait -p id %1`, nil, nil},
		{`unset PATH`, nil, []string{"rebind"}},
		{`unset -v x PATH`, nil, []string{"rebind"}},
		{`unset -f PATH; unset x; export PATH; export FOO=1; echo "$PATH"; GOPATH=/x go env`, nil, nil},

		// Declarations run as a command are no assignments to the parser.
		{`builtin export PS1='$(id)'`, nil, []string{"prompt"}},
		{`command export PATH=/tmp`, nil, []string{"rebind"}},
		{`\export PATH=/tmp`, nil, []string{"rebind"}},
		{`builtin declare -n r=PS1`, nil, []string{"prompt"}},
		{`builtin export -n PATH=/tmp`, nil, []string{"rebind"}},
		{`builtin local "$x"`, nil, []string{"computed"}},
		{`command export FOO=1`, nil, nil},

		{`fc -s`, nil, []string{"computed"}},
		{`fc -e - sudo`, nil, []string{"computed"}},
		{`fc -e vi`, nil, []string{"computed"}},
		{`fc`, nil, []string{"computed"}},
		{`fc -l -s`, nil, []string{"computed"}},
		{`fc -l; fc -l -10; fc -ln 1 5`, nil, nil},

		{`bash -c 'export PATH=/tmp'`, nil, []string{"rebind"}},
		{`eval 'hash -p /bin/rm ls'`, nil, []string{"rebind"}},
		{`trap 'bind -x "\"\C-a\": sudo ls"' EXIT`, [][]string{sudoLs}, []string{"prompt"}},
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

// The code of bind -x, complete -C and mapfile -C fails to parse as that
// of trap does.
func TestParseSettersError(t *testing.T) {
	for _, src := range []string{`bind -x '"\C-a": echo "'`, `complete -C 'echo "' x`, `mapfile -C 'echo "' a`} {
		if _, err := Parse(src, "", ""); err == nil {
			t.Errorf("%s: no parse error", src)
		}
	}
}

// With the example policy a changed PATH asks, and the code of bind -x is
// judged as any command.
func TestSettersExample(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, filepath.Join("testdata", "default"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want, reason string }{
		{`export PATH=/tmp:$PATH`, Ask, "the command is built at run time"},
		{`hash -p /bin/rm ls`, Ask, "the command is built at run time"},
		{`bind -x '"\C-a": sudo ls'`, Deny, "sudo is not allowed for the agent"},
		{`complete -C 'sudo ls' x`, Deny, "sudo is not allowed for the agent"},
		{`export FOO=1`, Allow, ""},
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
