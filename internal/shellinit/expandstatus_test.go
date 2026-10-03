package shellinit

import (
	"bytes"
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
// sent, neither expanded nor as typed, whether the user traps SIGINT or
// not. It stays on the screen and in history, and the shell takes the next
// line at once, at one prompt, with $? 130 and the user's trap back. A dim
// "expanding…" stands for the line erased while the substitution runs; a
// request with no $(...) goes without it.
func TestExpandInterrupt(t *testing.T) {
	const req = "Wait $(: >started; sleep 30) done"
	for _, tc := range []struct{ name, setup, trap string }{
		{"no trap", ":", ""},
		{"user trap", "trap 'echo user' INT", "trap -- 'echo user' SIGINT"},
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
			// Without job control a background job ignores SIGINT: it
			// outlives its kill, and waits for the substitution to start,
			// five seconds at most. fc lists the line before the printf.
			script := `AISH_BIN=fake
fake() { printf 'sent %s\x1f' "$*"; }
source ` + init + ` 2>/dev/null
Home $HOME here
` + tc.setup + `
(for __i in {1..100}; do [[ -e started ]] && break; sleep 0.05; done; kill -INT 0) &
printf 'before\x1f'
` + req + `
printf 'rc=%s\x1f' "$?"
fc -ln -2 -2
printf 'trap=%s\x1f' "$(trap -p INT)"
`
			cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
			cmd.Dir = dir
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = cleanEnv("PS1=", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8",
				"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
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

			// A primary prompt comes after each cmd-end.
			got := regexp.MustCompile("\x1b]6973;N;cmd-end;([0-9]+);[^\a]*\a").ReplaceAllString(string(out), "<end $1>")
			got = regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(got, "")
			if sent := regexp.MustCompile("sent [^\x1f]*").FindAllString(got, -1); len(sent) != 1 || sent[0] != "sent agent start -- Home "+dir+" here" {
				t.Errorf("sent %q, want only the request without $(...)", sent)
			}
			_, rest, _ := strings.Cut(got, "before\x1f")
			seg, _, ok := strings.Cut(rest, "rc=")
			if !ok {
				t.Fatalf("no request between before and rc= in %q", got)
			}
			var codes []string
			for _, m := range regexp.MustCompile(`<end ([0-9]+)>`).FindAllStringSubmatch(seg, -1) {
				codes = append(codes, m[1])
			}
			// The prompt the request is typed at, and the one after it.
			if strings.Join(codes, " ") != "0 130" {
				t.Errorf("cmd-end codes around the request %q, want [0 130] (in %q)", codes, seg)
			}
			if !strings.Contains(seg, req+"\n") {
				t.Errorf("the request is not left on the screen: %q", seg)
			}
			for _, want := range []string{"rc=130\x1f", "\t " + req + "\n", "trap=" + tc.trap + "\x1f"} {
				if !strings.Contains(got, want) {
					t.Errorf("output %q lacks %q", got, want)
				}
			}

			e := stderr.String()
			if n := strings.Count(e, "expanding…"); n != 1 {
				t.Errorf("expanding… shown %d times, want once, for the $(...) request", n)
			}
			if _, after, ok := strings.Cut(e, "\x1b[2mexpanding…\x1b[0m\r"); !ok {
				t.Errorf("no dim expanding… back at column 0 in %q", e)
			} else if redraw, _, _ := strings.Cut(after, "__aish_ask"); !strings.Contains(redraw, "\x1b[K") {
				t.Errorf("expanding… not erased before readline redraws the line: %q", redraw)
			}
		})
	}
}
