package shellinit

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// bashTyped starts bash in a PTY cols columns wide and 24 rows high, sources
// the rc aish gives it, with bashrc for the user's ~/.bashrc and inputrc for
// his ~/.inputrc, types the steps and then exit. It returns the home, where
// the shell runs, and all it printed. Readline draws as on a terminal only
// on one: with keys from a pipe it skips the redisplays typeahead makes
// moot, and what it erases with the line differs.
func bashTyped(t *testing.T, inputrc, bashrc string, cols int, steps ...zstep) (string, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".bashrc":  bashrc,
		".inputrc": inputrc,
		"rc":       RCFile(),
	}
	for name, s := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Sourced at the prompt, not by --rcfile: that one reads the system's
	// bashrc too.
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Env = cleanEnv("PS1=> ", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "TERM=xterm",
		"HOME="+dir, "INPUTRC="+filepath.Join(dir, ".inputrc"), "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"))
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: uint16(cols)})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var mu sync.Mutex
	var out bytes.Buffer
	read := make(chan struct{})
	go func() {
		defer close(read)
		b := make([]byte, 4096)
		for {
			n, err := f.Read(b)
			mu.Lock()
			out.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	printed := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
	steps = append([]zstep{{keys: "source " + filepath.Join(dir, "rc") + "\r"}}, steps...)
	deadline := time.Now().Add(20 * time.Second)
	for _, s := range append(steps, zstep{prompts: -1, keys: "exit\r"}) {
		for {
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				t.Fatalf("waiting for %d prompts before %q:\n%q", s.prompts, s.keys, printed())
			}
			// The first prompt, before the rc, has no cmd-end.
			if p := printed(); strings.Contains(p, "> ") && strings.Count(p, ";cmd-end;") >= s.prompts {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(100*time.Millisecond + s.delay)
		if _, err := io.WriteString(f, s.keys); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("bash did not exit:\n%q", printed())
	}
	f.Close()
	<-read
	return dir, printed()
}

// TestUnechoShowMode types a request three rows long at a prompt readline
// draws with the mode string of show-mode-in-prompt before its last line, in
// vi insert mode, in vi command mode after Esc and in emacs mode, and plays
// all bash printed on a screen, as TestLongEcho does. The echo of
// `__aish_ask "$__aish_req"` fits in a row without the mode string and wraps
// with it, wide characters taking two columns: none of it stays, on the
// screen or in its history. What the string has between \1 and \2 takes no
// columns, and the output above the echo stays.
func TestUnechoShowMode(t *testing.T) {
	const cols = 40
	const ps1 = `user@host:~\$ ` // 13 columns, and the echo's line 24
	long := "Explain " + strings.Repeat("why the sky is blue ", 5)
	for _, c := range []struct {
		name, inputrc, ps1 string
		esc                bool
	}{
		{"vi insert", "set editing-mode vi\n", ps1, false},
		{"vi command", "set editing-mode vi\n", ps1, true},
		{"emacs wide", "set emacs-mode-string \"漢字 \"\n", ps1, false},
		{"invisible", "set editing-mode vi\nset vi-ins-mode-string \"\\1\\e[1m\\e[0m\\2\"\n", ps1, false},
		{"bold", "set editing-mode vi\nset vi-ins-mode-string \"\\1\\e[1m\\2<ins>\\1\\e[0m\\2\"\n", ps1, false},
		{"two lines", "set editing-mode vi\n", `top\n` + ps1, false},
		{"off", "set editing-mode vi\nset show-mode-in-prompt off\n", ps1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			inputrc := "set show-mode-in-prompt on\n" + c.inputrc
			bashrc := `PS1='` + c.ps1 + `'
AISH_BIN=fake AISH_RUN=$HOME
printf 'N\n' >"$AISH_RUN/nonce"
fake() { [[ $2 == start ]] && printf '%s\x1f' "$4" >>sent; return 0; }
`
			steps := []zstep{{prompts: 1, keys: "echo above\r"}, {prompts: 2, keys: long}}
			if c.esc {
				steps = append(steps, zstep{prompts: 2, keys: "\x1b", delay: 600 * time.Millisecond})
			}
			// exit once the request is over, at the prompt after it.
			steps = append(steps, zstep{prompts: 2, keys: "\r"}, zstep{prompts: 3})
			dir, out := bashTyped(t, inputrc, bashrc, cols, steps...)
			s := newScreen(cols, 24)
			s.write(t, regexp.MustCompile("\x1b]6973;[^\a]*\a|\x1b\\[\\?2004[hl]").ReplaceAllString(out, ""))
			shown := append(append([]string(nil), s.history...), s.lines()...)
			for _, row := range shown {
				if strings.Contains(row, "__aish_ask") {
					t.Errorf("the echo stayed: %q", row)
				}
			}
			above := "\nabove\n"
			if strings.HasPrefix(c.ps1, "top") {
				above += "top\n"
			}
			text := "\n" + strings.Join(shown, "\n") + "\n"
			if want := above + strings.Join(rowsOf("user@host:~? "+strings.TrimSpace(long), cols), "\n") + "\n"; !strings.Contains(text, want) {
				t.Errorf("want the request right under the output above it:\n%s\nscreen:\n%s\noutput %q", want, text, out)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "sent")); string(b) != strings.TrimSpace(long)+"\x1f" {
				t.Errorf("sent %q", b)
			}
		})
	}
}
