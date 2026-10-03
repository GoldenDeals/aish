package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests here type their lines: an interactive bash reads its input
// with readline from a pipe too, and Enter runs __aish_route by bind -x,
// the prompt and its PROMPT_COMMAND between the lines.

// TestExpandStatus has $? in a request mean what it means at the prompt:
// the code of the user's last command, not that of the functions the
// expansion runs in.
func TestExpandStatus(t *testing.T) {
	const bashrc = `AISH_BIN=fake
fake() { [[ $2 == start ]] && printf 'sent %s\x1f' "$4"; }`
	cases := []struct{ cmd, in, want string }{
		{"false", "Код $?", "Код 1"},
		{"true", "Код $?", "Код 0"},
		{"(exit 7)", "Exit ${?} and $(echo $?)", "Exit 7 and 7"},
		{"false", "?Код $?", "Код $?"},
	}
	var script strings.Builder
	for _, c := range cases {
		script.WriteString(c.cmd + "\n" + c.in + "\n")
	}
	var got []string
	for _, s := range routed(t, "", bashrc, script.String()) {
		if _, sent, ok := strings.Cut(s, "sent "); ok {
			got = append(got, sent)
		}
	}
	for i, c := range cases {
		if i >= len(got) {
			t.Fatalf("missing request for %q after %q (got %q)", c.in, c.cmd, got)
		}
		if got[i] != c.want {
			t.Errorf("%q after %q: sent %q, want %q", c.in, c.cmd, got[i], c.want)
		}
	}
}

// TestExpandInterrupt interrupts a request in its slow $(...) the way
// Ctrl+C does, by SIGINT to the shell's process group: the request is not
// sent, neither expanded nor as typed, and the shell takes the next line
// at once.
func TestExpandInterrupt(t *testing.T) {
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
	// Without job control a background job ignores SIGINT: it outlives
	// its kill, and waits for the substitution to start, five seconds at
	// most.
	script := `AISH_BIN=fake
fake() { printf 'sent %s\x1f' "$*"; }
source ` + init + ` 2>/dev/null
(for __i in {1..100}; do [[ -e started ]] && break; sleep 0.05; done; kill -INT 0) &
Wait $(: >started; sleep 30) done
printf 'alive\x1f'
`
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = cleanEnv("PS1=", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8",
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run)
	// A group of its own: SIGINT to the test's would stop the test.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	start := time.Now()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("the shell took %v: the substitution was not interrupted", d)
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); err != nil {
		t.Errorf("the substitution did not start: %v", err)
	}
	got := regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(string(out), "")
	if want := "alive\x1f"; got != want {
		t.Errorf("output %q, want %q", got, want)
	}
}
