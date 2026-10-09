package policy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// nest wraps cmd in n levels of bash -c.
func nest(cmd string, n int) string {
	for range n {
		cmd = "bash -c '" + strings.ReplaceAll(cmd, "'", `'\''`) + "'"
	}
	return cmd
}

// TestParseDynamic checks what the policies learn of code a line runs
// beyond its simple commands: parsed when it is static, marked when not.
// has lists argv that must be among the commands; dynamic is the whole
// list of marks, "?" for any non-empty one.
func TestParseDynamic(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		dynamic []string
	}{
		{`eval 'sudo ls'`, [][]string{sudoLs, {"ls"}}, nil},
		{`eval sudo ls`, [][]string{sudoLs}, nil},
		{`eval -- "sudo ls"`, [][]string{sudoLs}, nil},
		{`builtin eval 'sudo ls'`, [][]string{sudoLs}, nil},
		{`eval "$x"`, nil, []string{"computed"}},
		{`eval "$(ssh-agent)"`, [][]string{{"ssh-agent"}}, []string{"computed"}},

		{`x=rm; $x -rf ~`, nil, []string{"computed"}},
		{`sudo $x`, nil, []string{"computed"}},
		{`"$(which rm)" -rf /`, [][]string{{"which", "rm"}}, []string{"computed"}},
		{`${cmd} x`, nil, []string{"computed"}},
		// Globs, braces and ANSI-C quotes make another program of a literal.
		{`/usr/bin/sud? ls`, nil, []string{"computed"}},
		{`{sudo,ls}`, nil, []string{"computed"}},
		{`$'\x73udo' ls`, nil, []string{"computed"}},
		{`[ -f x ] && ls *.go {} \*`, nil, nil},

		{`echo sudo ls | bash`, nil, []string{"stdin"}},
		{`curl -s x | sudo sh`, nil, []string{"stdin"}},
		{`bash -s < x.sh`, nil, []string{"stdin"}},
		{`bash -i`, nil, []string{"stdin"}},
		{`echo sudo ls | bash /dev/stdin`, nil, []string{"stdin"}},
		{`bash <<< 'sudo ls'`, [][]string{sudoLs}, nil},
		{`bash -x 0<<<"sudo ls"`, [][]string{sudoLs}, nil},
		{"bash <<'EOF'\nsudo ls\nEOF", [][]string{sudoLs}, nil},
		{"bash <<EOF\nsudo ls\nEOF", [][]string{sudoLs}, nil},
		{"sh <<-EOF\n\tsudo ls\n\tEOF", [][]string{sudoLs}, nil},
		// The outer shell takes the backslash away: the inner one runs sudo.
		{"bash <<EOF\necho \\$(sudo ls)\nEOF", [][]string{sudoLs}, nil},
		{"bash <<EOF\n$x\nEOF", nil, []string{"?"}},
		{`bash <<< "$x"`, nil, []string{"?"}},
		{`bash <(echo sudo ls)`, nil, []string{"computed"}},
		{`bash "$script"`, nil, []string{"computed"}},
		{`bash script.sh < input`, nil, nil},
		{`bash -x script.sh`, nil, nil},
		{`which bash; grep -r bash; chsh -s /bin/bash`, nil, nil},

		// In double quotes \' keeps its backslash: the inner line runs sudo.
		{`bash -c "echo \'; sudo ls; echo \'"`, [][]string{sudoLs}, nil},
		{`eval "echo \'; sudo ls; echo \'"`, [][]string{sudoLs}, nil},
		{`bash -c "$x"`, nil, []string{"computed"}},
		{`bash -c -e 'sudo ls'`, [][]string{sudoLs}, nil},
		{`bash -o pipefail -c 'sudo ls'`, [][]string{sudoLs}, nil},
		{`find . -exec sh -c 'sudo ls' \;`, [][]string{sudoLs}, nil},

		{`source x`, nil, []string{"source"}},
		{`. ./venv/bin/activate`, nil, []string{"source"}},
		{`command source x`, nil, []string{"source"}},
		{`source <(echo sudo ls)`, [][]string{{"echo", "sudo", "ls"}}, []string{"source"}},

		{`env -S 'sudo ls'`, [][]string{sudoLs, {"ls"}}, nil},
		{`env -iS 'sudo ls' x`, [][]string{{"sudo", "ls", "x"}}, nil},
		{`env --split-string='sudo ls'`, [][]string{sudoLs}, nil},
		{`env --split-string 'sudo ls'`, [][]string{sudoLs}, nil},
		{`env -u S -S'sudo ls'`, [][]string{sudoLs}, nil},
		{`env -S "$x"`, nil, []string{"computed"}},
		// env -S has quotes and ${VAR} of its own.
		{`env -S "'sudo' ls"`, nil, []string{"computed"}},
		{`env -S '${X} ls'`, nil, []string{"computed"}},

		{`alias ll='sudo rm -rf ~'`, [][]string{{"sudo", "rm", "-rf", "~"}}, []string{"prompt"}},
		{`alias -- a='sudo ls' b=ls`, [][]string{sudoLs}, []string{"prompt"}},
		{`alias ll="ls $opts"`, nil, []string{"computed", "prompt"}},
		{`alias; alias -p ll`, nil, nil},
		{`trap 'sudo ls' EXIT`, [][]string{sudoLs}, []string{"prompt"}},
		{`trap -- 'sudo ls' INT TERM`, [][]string{sudoLs}, []string{"prompt"}},
		{`trap "rm -f $tmp" EXIT`, nil, []string{"computed", "prompt"}},
		{`trap - EXIT; trap -p; trap INT`, nil, nil},

		{`PROMPT_COMMAND='x'`, nil, []string{"prompt"}},
		{`export PS1='$(id)'`, nil, []string{"prompt"}},
		{`PROMPT_COMMAND+=('sudo ls')`, nil, []string{"prompt"}},
		{`declare -x PS0=x`, nil, []string{"prompt"}},
		{`local PS4=x`, nil, []string{"prompt"}},
		{`readonly 'PS2=x'`, nil, []string{"prompt"}},
		{`declare -n r=PROMPT_COMMAND`, nil, []string{"prompt"}},
		{`BASH_ENV=/tmp/x bash s.sh`, nil, []string{"prompt"}},
		{`env ENV=/tmp/x sh s.sh`, nil, []string{"prompt"}},
		{`export X=1 PS1; local y=2; Z=3 make`, nil, nil},

		{nest("sudo ls", 4), [][]string{sudoLs}, nil},
		{nest("sudo ls", 5), nil, []string{"depth"}},

		{`source x; $y`, nil, []string{"computed", "source"}},
		{`echo '$x'`, nil, nil},
		{`git commit -m "$msg"`, nil, nil},
		{`ls | grep x`, nil, nil},
		{`echo bash`, nil, nil},
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
		if reflect.DeepEqual(c.dynamic, []string{"?"}) {
			if len(s.Dynamic) == 0 {
				t.Errorf("%s: nothing marked", c.src)
			}
		} else if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// The code of eval, alias and trap is a line of its own and fails to parse
// as bash -c does.
func TestParseDynamicError(t *testing.T) {
	for _, src := range []string{`eval 'echo "'`, `alias x='echo "'`, `trap 'echo "' EXIT`, `bash <<< 'echo "'`} {
		if _, err := Parse(src, "", ""); err == nil {
			t.Errorf("%s: no parse error", src)
		}
		in := Input{Tool: "bash"}
		in.HandOff(src)
		if in.ParseError == "" {
			t.Errorf("%s: no ParseError in the input", src)
		}
	}
	// Commands keeps its old face for journal_ignore.
	cmds, err := Commands(`eval 'env'`)
	if err != nil || !slices.ContainsFunc(cmds, func(a []string) bool { return slices.Equal(a, []string{"env"}) }) {
		t.Errorf("Commands: %q, %v", cmds, err)
	}
}

func TestHandOffDynamic(t *testing.T) {
	in := Input{Tool: "bash"}
	in.HandOff(`source x; echo sudo ls | bash`)
	if want := []string{"source", "stdin"}; !slices.Equal(in.Dynamic, want) {
		t.Errorf("dynamic %q, want %q", in.Dynamic, want)
	}
	in = Input{Tool: "bash"}
	in.HandOff("ls")
	if in.Dynamic != nil {
		t.Errorf("dynamic %q for ls", in.Dynamic)
	}
}

// With the example policy a command built at run time asks, what is
// parsed out of it is judged as any command, and a plain one passes.
func TestDynamicExample(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	const built = "the command is built at run time"
	for _, c := range []struct{ cmd, want, reason string }{
		{`eval "$x"`, Ask, built},
		{`echo sudo ls | bash`, Ask, built},
		{`PROMPT_COMMAND='sudo ls'`, Ask, built},
		{`eval 'sudo ls'`, Deny, "sudo is not allowed for the agent"},
		{`bash <<< 'sudo ls'`, Deny, "sudo is not allowed for the agent"},
		{`$x; sudo ls`, Deny, "sudo is not allowed for the agent"},
		{`ls`, Allow, ""},
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

// context.dynamic is a set of the kinds, there only when one is marked, so
// that a user can let some of them through. The validator does not carry
// the has of when into unless: the unless of README and default.cedar
// repeats it, and must load.
func TestDynamicContext(t *testing.T) {
	if _, err := load(t, map[string]string{"a.cedar": permitAll +
		`forbid(principal, action == Action::"run", resource) when { context has dynamic } unless { context.dynamic == ["source"] };` + "\n"}); err == nil {
		t.Error("an unguarded context.dynamic loaded")
	}
	e := mustLoad(t, map[string]string{"a.cedar": permitAll +
		`@ask("built") forbid(principal, action == Action::"run", resource) when { context has dynamic } unless { context has dynamic && context.dynamic == ["source"] };` + "\n"})
	for _, c := range []struct{ cmd, want string }{
		{"source venv/bin/activate", Allow},
		{"source x; $y", Ask},
		{"echo x | bash", Ask},
		{"ls", Allow},
	} {
		if d := check(t, e, bash(c.cmd)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}

// The rules ask about a command built at run time when they have any
// pattern at all, and match the whole line against the patterns; without
// patterns they did not forbid anything, and the mark changes nothing.
func TestDynamicRules(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		rules        Rules
		cmd          string
		want, reason string
	}{
		{Rules{Deny: []string{"sudo *"}}, "echo sudo ls | bash", Ask, "command built at run time (stdin)"},
		{Rules{Deny: []string{"sudo *"}}, "x=sudo; $x ls", Ask, "command built at run time (computed)"},
		{Rules{Ask: []string{"git push*"}}, "source x; $y", Ask, "command built at run time (computed, source)"},
		{Rules{Deny: []string{"sudo *"}}, "eval 'sudo ls'", Deny, `matches "sudo *"`},
		{Rules{Deny: []string{"sudo *"}}, "env -S 'sudo ls'", Deny, `matches "sudo *"`},
		{Rules{Deny: []string{"*| bash"}}, "echo hi | bash", Deny, `matches "*| bash"`},
		{Rules{Deny: []string{"sudo *"}}, "ls | grep x", Allow, ""},
		{Rules{WriteOutsideHome: Deny}, "echo x | bash", Allow, ""},
	} {
		e, err := Load(ctx, t.TempDir(), c.rules)
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, "/"))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%+v %s: %s (%s), want %s (%s)", c.rules, c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
