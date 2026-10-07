package shellinit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Under set -k (set -o keyword) bash takes every NAME=VALUE word of a
// simple command for an assignment to the command's environment, those
// after local too: `local x=v` declares nothing, and x is empty after it.

// TestKeywordRoute routes lines in a shell whose ~/.bashrc has set -k: the
// answers are those without it.
func TestKeywordRoute(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ls -la", "ls -la"},
		{"myfn arg", "myfn arg"},
		{"FOO=1 make", "FOO=1 make"},
		{"./run.sh", "./run.sh"},
		{"Как найти большие файлы?", "__aish_ask 'Как найти большие файлы?'"},
		{"Say $V now", "__aish_ask 'Say world now'"},
		{"gti status", "gti status"},
		{"?ls", "__aish_ask 'ls'"},
		{"? explain this", "__aish_ask 'explain this'"},
		{"@main.go what is it", "__aish_ask '@main.go what is it'"},
		{"!hello there", "hello there"},
		{"", ""},
	}
	var script strings.Builder
	for _, c := range cases {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; " + printLine + "\n")
	}
	script.WriteString("[[ $- == *k* ]] && printf 'k on'\n")
	got := routed(t, "", "set -k\nV=world\nmyfn() { :; }", script.String())
	for i, c := range cases {
		if i >= len(got) {
			t.Fatalf("missing output for %q (got %q)", c.in, got)
		}
		if got[i] != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got[i], c.want)
		}
	}
	if got[len(got)-1] != "k on" {
		t.Errorf("set -k is off after routing: %q", got[len(got)-1])
	}
}

// TestKeyword types lines at the prompt of a shell whose ~/.bashrc has
// set -k, as TestExpandStatus does: a request goes to the model, $? in it
// is the code of the user's last command, the agent's command runs in the
// shell and its code goes back, cmd-end and $? in PS1 carry the code of
// each command, a line that is no command gets bash's answer and 127, and
// set -k is still on after all that.
func TestKeyword(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	init := filepath.Join(dir, "init.bash")
	files := map[string]string{
		init:                               Bash,
		filepath.Join(run, "nonce"):        "N\n",
		filepath.Join(run, "route"):        "",
		filepath.Join(dir, "sub", ".keep"): "",
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// `agent start` of a request to run hands the shell a command that
	// changes it and fails; `agent resume` gets its code. The lines are
	// typed: no tabs, readline would complete them.
	script := `set -k
PS1='<rc=$?>\$ '
AISH_BIN=fake
fake() {
  case $2 in
  start)
    printf 'sent %s\x1f' "$4"
    [[ $4 == run* ]] || return 0
    printf id1 >"$AISH_RUN/next.id"
    printf '%s' 'cd sub && agent_var=set; (exit 3)' >"$AISH_RUN/next.cmd"
    ;;
  resume) printf 'resumed %s %s\x1f' "$3" "$4" ;;
  esac
}
source ` + init + ` 2>/dev/null
false
Код $?
(exit 7)
?run it
printf 'var=%s dir=%s\x1f' "$agent_var" "${PWD##*/}"
gti
[[ $- == *k* ]] && printf 'k on\x1f'
`
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = cleanEnv("PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8",
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	got := regexp.MustCompile("\x1b]6973;N;cmd-end;([0-9]*);[^\a]*\a").ReplaceAllString(string(out), "<end $1>")
	got = regexp.MustCompile("\x1b]6973;N;agent-end;([^;]*);([0-9]*);[^\a]*/([^/\a]*)\a").ReplaceAllString(got, "<agent-end $1 $2 $3>")
	got = regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(got, "")
	// In the order the lines were typed.
	rest := got
	for _, want := range []string{
		"<end 1>",
		"? Код 1\n", "sent Код 1\x1f", "<end 0>",
		"<end 7>",
		"? run it\n", "sent run it\x1f", "<agent-end id1 3 sub>", "resumed id1 3\x1f", "<end 0>",
		"var=set dir=sub\x1f", "<end 0>",
		"<end 127>",
		"k on\x1f",
	} {
		_, after, ok := strings.Cut(rest, want)
		if !ok {
			t.Fatalf("output lacks %q after what came before (all of it: %q)\nstderr:\n%s", want, got, stderr.String())
		}
		rest = after
	}

	// The prompt shows $? as the command left it, __aish_precmd before it.
	e := stderr.String()
	rest = e
	for _, want := range []string{"<rc=1>$ ", "<rc=7>$ ", "bash: gti: command not found\n", "<rc=127>$ "} {
		_, after, ok := strings.Cut(rest, want)
		if !ok {
			t.Fatalf("stderr lacks %q after what came before (all of it: %q)", want, e)
		}
		rest = after
	}
}
