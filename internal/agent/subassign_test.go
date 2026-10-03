package agent

import (
	"strings"
	"testing"
)

// Under Bash(...) patterns a line may not set the variables that change
// what its commands run: PATH=. git log runs ./git, GIT_EXTERNAL_DIFF and
// GIT_SSH_COMMAND make git run a program of the subagent's choice. The
// script's own variables, in lower case, and the locale's are set freely.
func TestScopedBashAssignments(t *testing.T) {
	dir := t.TempDir()
	git := &bashScope{patterns: []string{"git *"}}
	for _, cmd := range []string{
		"git log",
		`x=1; git log -n "$x"`,
		"LC_ALL=C git log",
		"LANG=C.UTF-8 TZ=UTC git log",
		"declare x=1; git log",
		"export x=1; git log",
		"declare -a xs=(a b); git log",
		`for f in a b; do git log -- "$f"; done`,
		"git log -n $((1 + 2))",
		"git log -n $((0x10))",
		"git log ${x:-HEAD}",
		`git log "${xs[0]}" "${xs[@]}" "${!xs[@]}"`,
		"git log ${x:1:2}",
		"(( x = 1 )); git log",
		"git log $(x=1 git rev-parse HEAD)",
	} {
		if why := refused(git, cmd, dir, nil); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	for cmd, want := range map[string]string{
		"PATH=. git log":                              "sets PATH",
		"GIT_EXTERNAL_DIFF=x git diff":                "sets GIT_EXTERNAL_DIFF",
		"export PATH=.; git log":                      "sets PATH",
		"env GIT_SSH_COMMAND=x git fetch":             "",
		"PATH+=:. git log":                            "sets PATH",
		"PATH[0]=.; git log":                          "sets PATH",
		"GIT_DIR=x; git log":                          "sets GIT_DIR",
		"declare -x GIT_CONFIG_GLOBAL=x; git log":     "sets GIT_CONFIG_GLOBAL",
		"typeset GIT_PAGER=x; git log":                "sets GIT_PAGER",
		"readonly PATH=.; git log":                    "sets PATH",
		"local PATH=.; git log":                       "sets PATH",
		"export 'PATH=.'; git log":                    "sets PATH",
		`export "$v"; git log`:                        "made at run time",
		"for PATH in .; do git log; done":             "sets PATH",
		"select PATH in .; do git log; done":          "sets PATH",
		"coproc PATH { git log; }":                    "sets PATH",
		"git log $(PATH=. git log)":                   "sets PATH",
		"git log <(GIT_DIR=x git log)":                "sets GIT_DIR",
		"git log ${GIT_DIR:=x}":                       "sets GIT_DIR",
		"git log ${PATH=.}":                           "sets PATH",
		"declare -n r=PATH; r=.; git log":             "-n",
		"nameref r=PATH; r=.; git log":                "nameref",
		"declare -i n; n=PATH=0; git log":             "-i",
		"local -in n; git log":                        "-in",
		"(( PATH = 0 )); git log":                     "sets PATH",
		"let PATH=0; git log":                         "sets PATH",
		"let x=PATH=0; git log":                       "sets PATH",
		"git log $((PATH = 0))":                       "sets PATH",
		"git log $[PATH = 0]":                         "sets PATH",
		"(( PATH++ )); git log":                       "sets PATH",
		"git log $(( a[PATH = 0] ))":                  "sets PATH",
		"x='PATH=0'; git log $((x))":                  "arithmetic on x",
		"x='PATH=0'; (( x += 1 )); git log":           "arithmetic on x",
		"x='PATH=0'; git log $(( $x ))":               "arithmetic on $x",
		"for ((i = 0; i < 3; i++)); do git log; done": "arithmetic on i",
		"x='PATH=0'; [[ x -eq 0 ]]; git log":          "arithmetic on x",
		`[[ "$x" -gt 1 ]] && git log`:                 `arithmetic on "$x"`,
		"[[ -v a[PATH=0] ]] && git log":               "-v",
		`[[ -v "$x" ]] && git log`:                    "-v",
		`x='a[PATH=0]'; git log "${!x}"`:              "${!x}",
		`git log "${a[PATH=0]}"`:                      "sets PATH",
		`x='PATH=0'; git log "${a[x]}"`:               "arithmetic on x",
		`x='PATH=0'; git log "${y:x}"`:                "arithmetic on x",
		`x='PATH=0'; git log "${y:0:x}"`:              "arithmetic on x",
		"a[PATH=0]=x; git log":                        "sets PATH",
		"a=([PATH=0]=x); git log":                     "sets PATH",
		"a=([x]=y); git log":                          "arithmetic on x",
	} {
		why := refused(git, cmd, dir, nil)
		if why == "" || !strings.Contains(why, want) {
			t.Errorf("%q: %q, want a refusal with %q in it", cmd, why, want)
		}
	}
	// The commands that set the variables their words name, when the
	// patterns let them run.
	more := &bashScope{patterns: []string{"git *", "env *", "builtin *", "command *", "export *", "declare *",
		"printf *", "read *", "mapfile *", "readarray *", "unset *", "getopts *", "wait *", "set *"}}
	for _, cmd := range []string{
		"env x=1 git log",
		"env -i git log",
		"env -ux git log",
		"builtin export x=1; git log",
		"printf '%s\\n' PATH; git log",
		"printf -v x %s y; git log",
		`read -r -p "Ref:" x <<< HEAD; git log "$x"`,
		"mapfile -t xs < /dev/null; git log",
		"unset x; git log",
		"getopts ab: opt; git log",
		"set -e; git log",
	} {
		if why := refused(more, cmd, dir, nil); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	for cmd, want := range map[string]string{
		"env GIT_SSH_COMMAND=x git fetch":      "sets GIT_SSH_COMMAND",
		"env -i PATH=. git log":                "sets PATH",
		"env -u PATH git log":                  "",
		"env -uPATH git log":                   "sets PATH",
		"env --unset=PATH git log":             "sets PATH",
		"env -S 'PATH=. git' log":              "sets PATH",
		`env "$v"=. git log`:                   "sets $v",
		"builtin export PATH=.; git log":       "sets PATH",
		"command declare -n r=PATH; git log":   "-n",
		"printf -v PATH .; git log":            "sets PATH",
		"printf -vPATH .; git log":             "sets PATH",
		"read PATH <<< .; git log":             "sets PATH",
		"read -a PATH <<< .; git log":          "sets PATH",
		"read -raPATH <<< .; git log":          "sets PATH",
		"read -p x PATH <<< .; git log":        "sets PATH",
		"mapfile PATH < /dev/null; git log":    "sets PATH",
		"readarray -t PATH < x; git log":       "sets PATH",
		"unset PATH; git log":                  "sets PATH",
		"getopts a PATH; git log":              "sets PATH",
		"wait -n -p PATH; git log":             "sets PATH",
		"read 'a[PATH=0]' <<< .; git log":      "sets a[PATH=0]",
		"set -k; git diff GIT_EXTERNAL_DIFF=x": "set -k",
		"set -o keyword; git log":              "set -k",
	} {
		why := refused(more, cmd, dir, nil)
		if why == "" || !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q in it", cmd, why, want)
		}
	}
	// The code of eval, a shell, trap, alias, su, flock, script and watch
	// has its commands checked by the policy's parser, and its variables
	// here.
	code := &bashScope{patterns: []string{"git *", "eval *", "bash *", "sh *", "trap *", "alias *", "su *",
		"flock *", "script *", "watch *"}}
	for _, cmd := range []string{
		"eval git log",
		"bash -c 'x=1; git log'",
		"trap 'git log' EXIT",
		"alias gl='git log'",
		"flock /tmp/l -c 'git log'",
		"watch 'git log'",
	} {
		if why := refused(code, cmd, dir, nil); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	for cmd, want := range map[string]string{
		"eval 'PATH=. git log'":              "sets PATH",
		"eval PATH=. git log":                "sets PATH",
		"bash -c 'GIT_DIR=x git log'":        "sets GIT_DIR",
		"sh -xc 'export PATH=.; git log'":    "sets PATH",
		"trap 'PATH=.' DEBUG; git log":       "sets PATH",
		"alias git='PATH=. git'; git log":    "sets PATH",
		"bash -c 'eval \"PATH=.\"; git log'": "sets PATH",
		"su -c 'PATH=. git log'":             "sets PATH",
		"flock /tmp/l -c 'PATH=. git log'":   "sets PATH",
		"script -q -c 'PATH=. git log'":      "sets PATH",
		"watch 'git log; GIT_DIR=x git log'": "sets GIT_DIR",
	} {
		why := refused(code, cmd, dir, nil)
		if why == "" || !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q in it", cmd, why, want)
		}
	}
	// The read-only bash of Grep, Glob and LS keeps its own rule, and
	// takes the arithmetic one too.
	ro := &bashScope{readOnly: true}
	for cmd, want := range map[string]string{
		"export PATH=.; cat a":   "export sets variables",
		"declare x=1; cat a":     "declare sets variables",
		"PATH=. cat a":           "sets PATH",
		"(( PATH = 0 )); cat a":  "sets PATH",
		"let PATH=0; cat a":      "sets PATH",
		"cat a $((PATH = 0))":    "sets PATH",
		"x='PATH=0'; cat $((x))": "arithmetic on x",
		"coproc PATH { cat a; }": "sets PATH",
		"cat ${PATH:=.}":         "sets PATH",
	} {
		if why := refused(ro, cmd, dir, nil); !strings.Contains(why, want) {
			t.Errorf("%q read-only: %q, want %q in it", cmd, why, want)
		}
	}
}
