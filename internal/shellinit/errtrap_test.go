package shellinit

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// errTrapShell types script at an interactive bash, with init.bash sourced
// after rc, the user's ~/.bashrc, or without it, and returns what bash
// printed, markers cut, and its exit code.
func errTrapShell(t *testing.T, rc, script string, aish bool) (string, int) {
	t.Helper()
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	init := filepath.Join(dir, "init.bash")
	files := map[string]string{
		init:                        Bash,
		filepath.Join(run, "nonce"): "N\n",
		filepath.Join(run, "route"): "",
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	load := ":"
	if aish {
		load = "source " + init + " 2>/dev/null"
	}
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("PS1='<rc=$?>\\$ '\n" + rc + "\n" + load + "\n" + script)
	// Readline draws on stderr; one pipe for both keeps the order.
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = cleanEnv("PS1=", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "TERM=dumb",
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run)
	err := cmd.Run()
	code := 0
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(out.String(), ""), code
}

// TestErrTrap has the user's ERR trap run once for a command that fails,
// as without aish, and not at all for a list that fails in its first
// command, though __aish_precmd returns that code again at the prompt
// after. The prompt and the user's PROMPT_COMMAND, a string or an array,
// still see the code. A trap that functions inherit by set -E runs no
// more often.
func TestErrTrap(t *testing.T) {
	dollar := "$"
	if os.Geteuid() == 0 {
		dollar = "#"
	}
	// Each line, the code it leaves for the prompt of the next one.
	lines := []struct {
		cmd string
		rc  string
	}{
		{"false", "1"},
		{"true", "0"},
		{"false && true", "1"}, // fails, but no trap: not the last of the list
		{"(exit 3)", "3"},
		{"echo end", "0"},
		{": last", ""},
	}
	var script strings.Builder
	for _, l := range lines {
		script.WriteString(l.cmd + "\n")
	}
	trapped := regexp.MustCompile(`(?m)^failed: [0-9]+$`)
	pcRan := regexp.MustCompile(`(?m)^pc[0-9] rc=[0-9]+$`)
	for _, pc := range []struct{ name, rc, want string }{
		{"no PROMPT_COMMAND", "unset PROMPT_COMMAND", ""},
		{"PROMPT_COMMAND string", `PROMPT_COMMAND='echo "pc1 rc=$?"'`, "pc1 rc=1"},
		{"PROMPT_COMMAND array", `PROMPT_COMMAND=('echo "pc1 rc=$?"' 'echo "pc2 rc=$?"')`, "pc1 rc=1\npc2 rc=1"},
	} {
		for _, trap := range []struct{ name, setup string }{
			{"ERR trap", `trap 'echo "failed: $?"' ERR`},
			{"ERR trap set -E", `set -E; trap 'echo "failed: $?"' ERR`},
		} {
			t.Run(pc.name+"/"+trap.name, func(t *testing.T) {
				rc := pc.rc + "\n" + trap.setup
				out, code := errTrapShell(t, rc, script.String(), true)
				bare, _ := errTrapShell(t, rc, script.String(), false)
				if code != 0 {
					t.Errorf("exit %d\n%q", code, out)
				}
				got, want := trapped.FindAllString(out, -1), []string{"failed: 1", "failed: 3"}
				if !slices.Equal(got, want) {
					t.Errorf("the trap printed %q, want %q\n%q", got, want, out)
				}
				if b := trapped.FindAllString(bare, -1); !slices.Equal(got, b) {
					t.Errorf("the trap printed %q, without aish %q", got, b)
				}
				for i, l := range lines[1:] {
					typed := "<rc=" + lines[i].rc + ">" + dollar + " " + l.cmd
					if !strings.Contains(out, typed) {
						t.Errorf("readline did not draw %q: %q", typed, out)
					}
				}
				if pc.want != "" && !strings.Contains(out, "\n"+pc.want+"\n") {
					t.Errorf("PROMPT_COMMAND after false printed no %q: %q", pc.want, out)
				}
				if got, b := pcRan.FindAllString(out, -1), pcRan.FindAllString(bare, -1); !slices.Equal(got, b) {
					t.Errorf("PROMPT_COMMAND printed %q, without aish %q", got, b)
				}
			})
		}
	}
}

// TestErrExit has set -e at an interactive bash close it at the line it
// closes it at without aish, and at no line before: a list that fails
// without exiting would close it at the prompt after, as __aish_precmd
// returns the list's code.
func TestErrExit(t *testing.T) {
	const script = "set -e\nfalse && true\necho alive1\n(exit 3) && :\necho alive2\nfalse\necho alive3\n"
	alive := regexp.MustCompile(`(?m)^alive[0-9]$`)
	for _, pc := range []struct{ name, rc string }{
		{"no PROMPT_COMMAND", "unset PROMPT_COMMAND"},
		{"PROMPT_COMMAND string", `PROMPT_COMMAND='echo "pc1 rc=$?"'`},
		{"PROMPT_COMMAND array", `PROMPT_COMMAND=('echo "pc1 rc=$?"' 'echo "pc2 rc=$?"')`},
	} {
		t.Run(pc.name, func(t *testing.T) {
			out, code := errTrapShell(t, pc.rc, script, true)
			bare, bareCode := errTrapShell(t, pc.rc, script, false)
			got, want := alive.FindAllString(out, -1), []string{"alive1", "alive2"}
			if !slices.Equal(got, want) {
				t.Errorf("printed %q, want %q\n%q", got, want, out)
			}
			if b := alive.FindAllString(bare, -1); !slices.Equal(got, b) {
				t.Errorf("printed %q, without aish %q", got, b)
			}
			if code != 1 || code != bareCode {
				t.Errorf("exit %d, without aish %d, want 1", code, bareCode)
			}
		})
	}
}
