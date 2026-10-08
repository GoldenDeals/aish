package shellinit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// seteRC is the user's ~/.bashrc of TestSetE before its options: `agent
// start` records the request and hands the shell a command, or fails as
// the client does after Ctrl+C; `agent resume` records the command's code
// and hands the next one, if any.
const seteRC = `AISH_BIN=fake
fake() {
	case $2 in
	start)
		printf '%s\x1f' "$4" >>"$HOME/sent"
		case $4 in
		'Run commands') hand id1 false ;;
		'Stop early') hand id3 'return 3' ;;
		'Wait for it') hand id4 ': >"$HOME/started2"; sleep 30' ;;
		'Interrupted') return 130 ;;
		esac
		;;
	resume)
		printf '%s=%s\x1f' "$3" "$4" >>"$HOME/resumed"
		[[ $3 != id1 ]] || hand id2 'cd sub && echo "in ${PWD##*/}"'
		;;
	esac
}
hand() { printf '%s\n' "$1" >"$AISH_RUN/next.id"; printf %s "$2" >"$AISH_RUN/next.cmd"; }
PS1='<rc=$?> '
`

// seteShell starts bash in a home of its own with bashrc as the user's
// ~/.bashrc, sources the rc file aish gives bash and types script at its
// prompt; env goes over the environment it has. It returns the home, all
// bash printed, markers kept, and its exit code.
func seteShell(t *testing.T, bashrc, script string, env ...string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".bashrc":   bashrc,
		"rc":        RCFile(),
		"run/nonce": "N\n",
		"run/route": "",
	}
	for name, s := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Sourced at the prompt, not by --rcfile: that one reads the system's
	// bashrc too.
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("source " + filepath.Join(dir, "rc") + "\n" + script)
	// Readline draws on stderr; one pipe for both keeps the order.
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(cleanEnv("PS1=", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "TERM=dumb",
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+filepath.Join(dir, "run")), env...)
	// A group of its own: SIGINT to the test's would stop the test.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	code := 0
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return dir, out.String(), code
}

// TestSetE has the user's set -e or set -u from ~/.bashrc, one that ends in
// a list that fails as many do, close no shell aish runs: not at the rc
// file, not at a line routed to bash or to the model, a command the agent
// hands the shell that fails or returns, a request the client ends with
// 130 or Ctrl+C ends in its expansion or its command. $? at the prompt
// after each is its code, as without the options; under set -u a request
// with a variable that is not set goes as typed.
func TestSetE(t *testing.T) {
	// Each line, the code it leaves for the prompt after it. Ctrl+C is
	// SIGINT to the shell's process group, sent once the command it
	// interrupts has started. Without job control a background job ignores
	// SIGINT: it outlives its kill.
	lines := []struct{ cmd, rc string }{
		{"echo alive1", "0"},
		{"!echo forced", "0"},
		{"for x in 1; do\necho cont $x\ndone", "0"},
		{"Run commands", "0"},
		{`printf 'rc=%s pwd=%s\n' "$?" "${PWD##*/}"`, "0"},
		{"Interrupted", "130"},
		{`printf 'rc=%s\n' "$?"`, "0"},
		{"Stop early", "3"},
		{`printf 'rc=%s\n' "$?"`, "0"},
		{"(exit 7) && :", "7"},
		{"Code $?", "0"},
		{"?Raw $HOME", "0"},
		{"Say $NOPE now", "0"},
		{"(for __i in {1..100}; do [[ -e $HOME/started2 ]] && break; sleep 0.05; done; kill -INT 0) &", "0"},
		{"Wait for it", "130"},
		{`printf 'rc=%s\n' "$?"`, "0"},
		{"(for __i in {1..100}; do [[ -e $HOME/started1 ]] && break; sleep 0.05; done; kill -INT 0) &", "0"},
		{`Wait $(: >"$HOME/started1"; sleep 30) done`, "130"},
		{`printf 'rc=%s\n' "$?"`, "0"},
		{"echo alive2", "0"},
	}
	var script strings.Builder
	codes := []string{"0"} // the source of the rc file
	for _, l := range lines {
		script.WriteString(l.cmd + "\n")
		codes = append(codes, l.rc)
	}
	for _, tc := range []struct{ name, opts string }{
		{"none", ":"},
		{"set -e", "set -e"},
		{"set -e inherit_errexit", "set -e; shopt -s inherit_errexit"},
		{"set -u", "set -u"},
		{"set -eu", "set -eu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bashrc := seteRC + tc.opts + "\n[ -f ~/.bash_aliases ] && . ~/.bash_aliases\n"
			start := time.Now()
			dir, out, code := seteShell(t, bashrc, script.String())
			if d := time.Since(start); d > 20*time.Second {
				t.Errorf("the shell took %v: a sleep was not interrupted", d)
			}
			if code != 0 {
				t.Errorf("exit %d\n%q", code, out)
			}
			plain := regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(out, "")
			for _, want := range []string{
				"\nalive1\n", "\nforced\n", "\ncont 1\n", "\nin sub\n", "\nrc=0 pwd=sub\n", "\nalive2\n",
				// $? in PS1 after a request.
				"<rc=0> printf 'rc=%s pwd=%s\\n' \"$?\" \"${PWD##*/}\"",
				"<rc=130> printf 'rc=%s\\n' \"$?\"",
				"<rc=3> printf 'rc=%s\\n' \"$?\"",
			} {
				if !strings.Contains(plain, want) {
					t.Errorf("no %q in %q", want, plain)
				}
			}
			var got []string
			for _, m := range regexp.MustCompile("\x1b]6973;N;cmd-end;([0-9]+);").FindAllStringSubmatch(out, -1) {
				got = append(got, m[1])
			}
			if strings.Join(got, " ") != strings.Join(codes, " ") {
				t.Errorf("cmd-end codes %q, want %q", got, codes)
			}

			var ends []string
			for _, m := range regexp.MustCompile("\x1b]6973;N;agent-end;([^;]*);([0-9]+);([^\a]*)\a").FindAllStringSubmatch(out, -1) {
				ends = append(ends, m[1]+"="+m[2]+" "+filepath.Base(m[3]))
			}
			if got, want := strings.Join(ends, ", "), "id1=1 "+filepath.Base(dir)+", id2=0 sub"; got != want {
				t.Errorf("agent-end %s, want %s", got, want)
			}

			read := func(name string) string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			nope := "Say  now"
			if strings.Contains(tc.opts, "u") {
				nope = "Say $NOPE now"
			}
			// The request whose expansion Ctrl+C ended is not sent.
			sent := strings.Join([]string{"Run commands", "Interrupted", "Stop early", "Code 7", "Raw $HOME", nope,
				"Wait for it", ""}, "\x1f")
			if got := read("sent"); got != sent {
				t.Errorf("sent %q, want %q", got, sent)
			}
			if got, want := read("resumed"), "id1=1\x1fid2=0\x1f"; got != want {
				t.Errorf("resumed %q, want %q", got, want)
			}
		})
	}
}

// TestAgentErrTrap has the user's ERR trap, one functions inherit by set
// -E, run for no command of a request that fails in none: the agent's
// command is read whole, to the end of its file.
func TestAgentErrTrap(t *testing.T) {
	for _, opts := range []string{"set -E", "set -eE"} {
		t.Run(opts, func(t *testing.T) {
			bashrc := `AISH_BIN=fake
fake() { [[ $2 != start ]] || { printf 'id\n' >"$AISH_RUN/next.id"; printf 'echo ran' >"$AISH_RUN/next.cmd"; }; }
` + opts + "\ntrap 'echo \"trapped $?\"' ERR\n"
			_, out, code := seteShell(t, bashrc, "Run it\necho alive\n")
			out = regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(out, "")
			if code != 0 || !strings.Contains(out, "\nran\n") || !strings.Contains(out, "\nalive\n") {
				t.Errorf("exit %d\n%q", code, out)
			}
			if strings.Contains(out, "trapped") {
				t.Errorf("the ERR trap ran: %q", out)
			}
		})
	}
}

// TestUnechoSetE has __aish_unecho put the request in place of readline's
// echo under set -e, of the line that is a list there: with the prompt it
// takes a row more than the one without, and none of it stays on the
// screen. Under set -u it does so with no PS1 too.
func TestUnechoSetE(t *testing.T) {
	const cols, rows = 30, 10
	for _, tc := range []struct{ name, opts, want string }{
		{"set -e", "PS1='> '\nset -e", "> ? Hi there"},
		{"set -u no PS1", "unset PS1\nset -u", "? Hi there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bashrc := "AISH_BIN=fake\nfake() { printf '%s\\x1f' \"$4\" >>\"$HOME/sent\"; }\n" + tc.opts + "\n"
			dir, out, code := seteShell(t, bashrc, "Hi there\nfc -ln 1 >\"$HOME/hist\"\n",
				"TERM=xterm", fmt.Sprintf("COLUMNS=%d", cols), fmt.Sprintf("LINES=%d", rows))
			if code != 0 {
				t.Errorf("exit %d\n%q", code, out)
			}
			if b, err := os.ReadFile(filepath.Join(dir, "sent")); err != nil || string(b) != "Hi there\x1f" {
				t.Errorf("sent %q (%v), want the request", b, err)
			}
			// What was typed, not the line HISTIGNORE keeps out.
			if b, err := os.ReadFile(filepath.Join(dir, "hist")); err != nil ||
				!strings.Contains(string(b), "\t Hi there\n") || strings.Contains(string(b), "__aish_ask") {
				t.Errorf("history %q (%v)", b, err)
			}
			s := newScreen(cols, rows)
			s.write(t, regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(out, ""))
			var asked bool
			for _, row := range append(append([]string(nil), s.history...), s.lines()...) {
				if strings.Contains(row, "__aish_ask") || strings.Contains(row, "&& :") {
					t.Errorf("the echo stayed: %q", row)
				}
				asked = asked || row == tc.want
			}
			if !asked {
				t.Errorf("no %q on the screen %q", tc.want, s.lines())
			}
		})
	}
}
