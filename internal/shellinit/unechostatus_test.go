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

// TestUnechoStatus types requests, after ? and routed, after commands that
// fail and succeed, at a prompt that shows $?, and plays all bash printed
// on a screen, as TestLongEcho does. The prompt __aish_unecho draws again
// in place of the echo has the code the prompt the request was typed at
// had, that of the user's last command, not 0 of the functions it is drawn
// in; a request with $? in it gets the same code. Setting that code runs no
// ERR trap of the user's, one functions inherit by set -E too.
func TestUnechoStatus(t *testing.T) {
	const cols, rows = 80, 24
	for _, tc := range []struct{ name, setup string }{
		{"no trap", ":"},
		{"ERR trap", "set -E; trap 'echo trapped' ERR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			script := `AISH_BIN=fake
fake() { :; }
PS1='<rc=$?>\$ '
source ` + init + ` 2>/dev/null
` + tc.setup + `
false
?hi
true
?hi
(exit 7)
Code $?
false
Hi there
`
			cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
			cmd.Dir = dir
			cmd.Stdin = strings.NewReader(script)
			// Readline draws on stderr; one pipe for both keeps the order.
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			cmd.Env = cleanEnv("PS1=", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "TERM=xterm",
				"COLUMNS=80", "LINES=24",
				"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run)
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			// The prompts the requests were typed at, before the redraw.
			dollar := "$"
			if os.Geteuid() == 0 {
				dollar = "#"
			}
			for _, typed := range []string{"<rc=1>" + dollar + " ?hi", "<rc=7>" + dollar + " Code $?"} {
				if !strings.Contains(out.String(), typed) {
					t.Errorf("readline did not draw %q: %q", typed, out.String())
				}
			}
			// What __aish_unecho drew after each echo: the line after its
			// erase, \e[J\e[A.
			want := []string{"<rc=1>? hi", "<rc=0>? hi", "<rc=7>? Code 7", "<rc=1>? Hi there"}
			var drawn []string
			for _, redraw := range strings.Split(out.String(), `__aish_ask "$__aish_req"`)[1:] {
				if before, _, _ := strings.Cut(redraw, "? "); strings.Contains(before, "trapped") {
					t.Errorf("the ERR trap ran in the redraw: %q", redraw)
				}
				if _, line, ok := strings.Cut(redraw, "\x1b[J\x1b[A"); ok {
					line, _, _ = strings.Cut(line, "\n")
					drawn = append(drawn, line)
				}
			}
			if strings.Join(drawn, "\n") != strings.Join(want, "\n") {
				t.Errorf("requests drawn %q, want %q", drawn, want)
			}
			if tc.setup != ":" {
				return // the trap's lines take escapes the screen does not know
			}
			s := newScreen(cols, rows)
			s.write(t, regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(out.String(), ""))
			var asked []string
			for _, row := range append(append([]string(nil), s.history...), s.lines()...) {
				if strings.Contains(row, "__aish_ask") {
					t.Errorf("the echo stayed: %q", row)
				}
				if strings.Contains(row, "? ") {
					asked = append(asked, row)
				}
			}
			if strings.Join(asked, "\n") != strings.Join(want, "\n") {
				t.Errorf("requests on the screen %q, want %q\nscreen %q", asked, want, s.lines())
			}
		})
	}
}
