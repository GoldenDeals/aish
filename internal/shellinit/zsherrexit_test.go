package shellinit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// errExitStub is AISH_BIN for TestZshErrExit. It writes how it is called
// to called in the home. `agent start` of a request that starts with Fail
// hands the shell a command that fails; of one that starts with Stop it
// fails itself, as after Ctrl+C.
const errExitStub = `#!/bin/sh
printf '%s\n' "$*" >>"$HOME/called"
case "$2 $4" in
"start Fail"*)
	printf 'id1\n' >"$AISH_RUN/next.id"
	printf 'false; : >"$HOME/ran"; false' >"$AISH_RUN/next.cmd"
	;;
"start Stop"*) exit 130 ;;
esac
exit 0
`

// TestZshErrExit types requests in a zsh whose .zshrc set err_exit,
// err_return or both, and the shell lives on through them: one the agent
// answers, one whose command fails, all of it running, one that fails
// itself, one typed after that, $? in it 130, &NAME of no subagent, and
// one whose expansion fails, which goes as typed. The line after a request
// runs with $? its code, as without the options, and so does the line
// after a prompt that sourced a restore.bash that fails. The user's ZERR
// trap runs for a request that failed, as for a command, but under
// err_exit for none: there the request is a list, as in bash under set -e.
func TestZshErrExit(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(stub, []byte(errExitStub), 0o755); err != nil {
		t.Fatal(err)
	}
	const after = `print -r -- "after $?" >>out` + "\r"
	for _, tc := range []struct{ opts, zerr string }{
		{"errexit", ""},
		{"errreturn", "130\n130\n2\n"},
		{"errexit errreturn", ""},
	} {
		t.Run(tc.opts, func(t *testing.T) {
			dir, printed := zshTyped(t, "setopt "+tc.opts+"\nAISH_BIN="+quote(stub)+"\nTRAPZERR() { print -r -- $? >>\"$HOME/zerr\" }\n",
				zstep{prompts: 1, keys: "What is this\r"},
				zstep{prompts: 2, keys: after},
				zstep{prompts: 3, keys: "Fail at it\r"},
				zstep{prompts: 4, keys: after},
				zstep{prompts: 5, keys: "Stop it\r"},
				zstep{prompts: 6, keys: after},
				zstep{prompts: 7, keys: "Stop it\r"},
				zstep{prompts: 8, keys: "Code was $?\r"},
				zstep{prompts: 9, keys: "&nobody do it\r"},
				zstep{prompts: 10, keys: after},
				zstep{prompts: 11, keys: "Expand $(print -r -- x \\y\r"},
				zstep{prompts: 12, keys: after},
				zstep{prompts: 13, keys: `print -r -- 'unfunction -- __aish_none' >"$AISH_RUN/restore.bash"` + "\r"},
				zstep{prompts: 14, keys: after},
				zstep{prompts: 15},
			)
			b, _ := os.ReadFile(filepath.Join(dir, "called"))
			want := "agent start -- What is this\nagent start -- Fail at it\nagent resume id1 1\nagent start -- Stop it\n" +
				"agent start -- Stop it\nagent start -- Code was 130\nagent start -- Expand $(print -r -- x \\y\n"
			if string(b) != want {
				t.Errorf("called\n%s\nwant\n%s\n%q", b, want, printed)
			}
			if _, err := os.Stat(filepath.Join(dir, "ran")); err != nil {
				t.Errorf("the agent's command stopped at its first failure: %v", err)
			}
			if o, _ := os.ReadFile(filepath.Join(dir, "out")); string(o) != "after 0\nafter 0\nafter 130\nafter 2\nafter 0\nafter 0\n" {
				t.Errorf("after the requests %q", o)
			}
			if n := len(regexp.MustCompile(";ask-start\a").FindAllString(printed, -1)); n != 6 {
				t.Errorf("%d ask-start markers, want 6", n)
			}
			if !strings.Contains(printed, "\x1b[A? Code was 130\r\n") {
				t.Errorf("the request after one that failed is not drawn:\n%q", printed)
			}
			if z, _ := os.ReadFile(filepath.Join(dir, "zerr")); string(z) != tc.zerr {
				t.Errorf("the user's ZERR trap ran with %q, want %q", z, tc.zerr)
			}
		})
	}
}

// TestZshErrExitUnecho: under err_exit the line zle leaves is longer by its
// ` && :`, and so is the echo __aish_unecho erases.
func TestZshErrExitUnecho(t *testing.T) {
	prompt := quote(strings.Repeat("x", 32) + "% ") // 58 columns of 60 with the line, 63 with the list
	got, _ := zshRun(t, "", "", "COLUMNS=60 __aish_unecho_draw text "+prompt+"; print -n $'\\x1f'\n"+
		"COLUMNS=60 __aish_unecho_draw text "+prompt+" on; print -n $'\\x1f'\n")
	for i, up := range []string{"\x1b[1A", "\x1b[2A"} {
		if !strings.HasPrefix(at(got, i), up+"\r") {
			t.Errorf("%d: %q, want it up %q", i, at(got, i), up)
		}
	}
}

// TestZshErrExitEsc: under the user's err_exit Esc stops the agent's
// command and the agent goes on, and Ctrl+C ends the request, as without
// it, and the shell lives on.
func TestZshErrExitEsc(t *testing.T) {
	const (
		probe  = `{ print -r -- "rc=$?" } >"$HOME/PROBE" 2>&1`
		sleeps = `: >"$HOME/started"; sleep 30; echo after >>"$HOME/ran"`
	)
	for _, tc := range []struct {
		name               string
		esc                bool
		called, after, ran string
	}{
		{"Esc", true, "agent start -- Wait for it\nagent resume id1 130\nagent resume id2 0\n", "rc=0\n", "next\n"},
		{"Ctrl+C", false, "agent start -- Wait for it\n", "rc=130\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, printed := escTyped(t, "zsh", "setopt err_exit\n", sleeps, probe, tc.esc)
			if b, _ := os.ReadFile(filepath.Join(dir, "called")); string(b) != tc.called {
				t.Errorf("called\n%s\nwant\n%s\n%q", b, tc.called, printed)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "probe2")); string(b) != tc.after {
				t.Errorf("after the request %q, want %q", b, tc.after)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "ran")); string(b) != tc.ran {
				t.Errorf("ran %q, want %q", b, tc.ran)
			}
		})
	}
}
