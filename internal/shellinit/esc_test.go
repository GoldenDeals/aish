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
	"golang.org/x/sys/unix"
)

// escStub is AISH_BIN for the tests of Esc. It writes how it is called to
// called in the home and hands the shell the commands of a request: the
// one in cmd1 in the home as id1, then, once that is resumed, one that
// writes "next" to ran as id2.
const escStub = `#!/bin/sh
echo "$@" >>"$HOME/called"
case $2 in
start)
	printf 'id1\n' >"$AISH_RUN/next.id"
	cat "$HOME/cmd1" >"$AISH_RUN/next.cmd"
	;;
resume)
	if [ "$3" = id1 ]; then
		printf 'id2\n' >"$AISH_RUN/next.id"
		printf 'echo next >>"$HOME/ran"' >"$AISH_RUN/next.cmd"
	fi
	;;
esac
`

// escTyped starts shell, bash or zsh, as aish does, on a terminal of its
// own with job control, rc in the user's rc file. It types probe, which
// writes to the file probe1, and a request whose first command is cmd;
// once the command has made started in the home, it does what the proxy
// does on Esc when esc, "id1 130" in $AISH_RUN/esc, and in either case
// sends SIGINT to the foreground of the terminal, as Ctrl+C would. Then it
// types probe for probe2, and exit. It returns the home and all the shell
// printed.
func escTyped(t *testing.T, shell, rc, cmd, probe string, esc bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	stub := filepath.Join(dir, "aish-stub")
	files := map[string]string{
		filepath.Join(run, "nonce"):    "N\n",
		filepath.Join(run, "route"):    expandOn,
		filepath.Join(run, "next.cmd"): "",
		filepath.Join(dir, "cmd1"):     cmd,
		stub:                           escStub,
	}
	env := cleanEnv("TERM=xterm", "LC_ALL=C.UTF-8", "PS1=> ", "HOME="+dir,
		"HISTFILE="+filepath.Join(dir, ".hist"), "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"),
		"AISH_RUN="+run, "AISH_BIN="+stub)
	var c *exec.Cmd
	var keys []string // typed one at each prompt
	switch shell {
	case "bash":
		files[filepath.Join(dir, ".bashrc")] = "PS1='> '\n" + rc
		files[filepath.Join(dir, "rc")] = RCFile()
		// Sourced at the prompt, not by --rcfile: that one reads the
		// system's bashrc too. The prompt before it prints no cmd-end.
		c = exec.Command("bash", "--norc", "--noprofile", "-i")
		keys = []string{"source " + filepath.Join(dir, "rc") + "\r"}
	case "zsh":
		files[filepath.Join(run, "zsh", ".zshenv")] = ZshEnv
		files[filepath.Join(run, "zsh", ".zshrc")] = ZshRC()
		files[filepath.Join(dir, ".zshrc")] = "PS1='> '\n" + rc
		c = exec.Command(zshPath(t), "-i")
		env = append(env, "ZDOTDIR="+filepath.Join(run, "zsh"))
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c.Dir, c.Env = dir, env
	f, err := pty.StartWithSize(c, &pty.Winsize{Rows: 24, Cols: 80})
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
	deadline := time.Now().Add(20 * time.Second)
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for !ok() {
			if time.Now().After(deadline) {
				c.Process.Kill()
				t.Fatalf("waiting for %s:\n%q", what, printed())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	prompts := func(n int) func() bool {
		return func() bool { return strings.Count(printed(), "\x1b]6973;N;cmd-end;") >= n }
	}
	typeKeys := func(s string) {
		t.Helper()
		time.Sleep(100 * time.Millisecond) // readline has the terminal
		if _, err := io.WriteString(f, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range keys {
		typeKeys(k)
	}
	waitFor("the first prompt", prompts(1))
	typeKeys(strings.ReplaceAll(probe, "PROBE", "probe1") + "\r")
	waitFor("the probe before", prompts(2))
	typeKeys("Wait for it\r")
	waitFor("the command to start", func() bool {
		_, err := os.Stat(filepath.Join(dir, "started"))
		return err == nil
	})
	time.Sleep(100 * time.Millisecond)
	if esc {
		if err := os.WriteFile(filepath.Join(run, "esc"), []byte("id1 130\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var group int
	if err := raw.Control(func(fd uintptr) { group, err = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) }); err != nil || group <= 0 {
		t.Fatalf("no foreground group: %v", err)
	}
	if err := unix.Kill(-group, unix.SIGINT); err != nil {
		t.Fatal(err)
	}
	waitFor("the prompt after the request", prompts(3))
	typeKeys(strings.ReplaceAll(probe, "PROBE", "probe2") + "\r")
	waitFor("the probe after", prompts(4))
	typeKeys("exit\r")
	done := make(chan error)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		c.Process.Kill()
		t.Fatalf("%s did not exit:\n%q", shell, printed())
	}
	f.Close()
	<-read
	return dir, printed()
}

// TestEsc stops the agent's command the way the proxy does on Esc, with
// "id1 130" in $AISH_RUN/esc and SIGINT to the terminal's foreground: the
// command, whether a program or a loop of the shell's own, ends with
// agent-end and 130, the rest of its line does not run, and the agent is
// resumed and hands the next command, which runs. $? after the request is
// its code, 0, and the user's trap on SIGINT is as before. Without the
// file the signal is Ctrl+C's: the request ends there, as it did.
func TestEsc(t *testing.T) {
	probes := map[string]string{
		"bash": `{ printf 'rc=%s\n' "$?"; trap -p INT; } >"$HOME/PROBE" 2>&1`,
		"zsh":  `{ print -r -- "rc=$?"; trap; } >"$HOME/PROBE" 2>&1`,
	}
	const (
		sleeps = `: >"$HOME/started"; sleep 30; echo after >>"$HOME/ran"`
		loops  = `: >"$HOME/started"; while :; do :; done; echo after >>"$HOME/ran"`
		trap   = `trap 'echo user >>"$HOME/trapped"' INT` + "\n"
	)
	for _, shell := range []string{"bash", "zsh"} {
		for _, tc := range []struct {
			name, rc, cmd string
			esc           bool
		}{
			{"sleep", "", sleeps, true},
			{"loop of the shell", "", loops, true},
			{"user trap", trap, `: >"$HOME/started"; sleep 30`, true},
			{"errexit", "set -e\n", sleeps, true},
			{"Ctrl+C", "", sleeps, false},
		} {
			if shell == "zsh" && tc.name == "errexit" {
				tc.rc = "setopt err_return\n"
			}
			t.Run(shell+"/"+tc.name, func(t *testing.T) {
				start := time.Now()
				dir, printed := escTyped(t, shell, tc.rc, tc.cmd, probes[shell], tc.esc)
				if d := time.Since(start); d > 15*time.Second {
					t.Errorf("took %v: the command was not stopped", d)
				}
				called, _ := os.ReadFile(filepath.Join(dir, "called"))
				ran, _ := os.ReadFile(filepath.Join(dir, "ran"))
				ends := regexp.MustCompile("\x1b]6973;N;agent-end;([^;]*);([0-9]+);").FindAllStringSubmatch(printed, -1)
				var got []string
				for _, m := range ends {
					got = append(got, m[1]+"="+m[2])
				}
				wantCalled, wantRan, wantEnds, wantRC := "agent start -- Wait for it\nagent resume id1 130\nagent resume id2 0\n", "next\n", "id1=130 id2=0", "rc=0\n"
				if shell == "bash" {
					// bash threw the line away and gives $? back after
					// PROMPT_COMMAND, which went on with the request.
					wantRC = "rc=130\n"
				}
				if !tc.esc {
					wantCalled, wantRan, wantEnds, wantRC = "agent start -- Wait for it\n", "", "", "rc=130\n"
				}
				if string(called) != wantCalled {
					t.Errorf("called\n%s\nwant\n%s", called, wantCalled)
				}
				if string(ran) != wantRan {
					t.Errorf("ran %q, want %q", ran, wantRan)
				}
				if s := strings.Join(got, " "); s != wantEnds {
					t.Errorf("agent-end %q, want %q", s, wantEnds)
				}
				p1, _ := os.ReadFile(filepath.Join(dir, "probe1"))
				p2, _ := os.ReadFile(filepath.Join(dir, "probe2"))
				before := strings.TrimPrefix(string(p1), "rc=0\n")
				if !strings.HasPrefix(string(p2), wantRC) || strings.TrimPrefix(string(p2), wantRC) != before {
					t.Errorf("after the request:\n%s\nwant %s and the trap as before:\n%s", p2, wantRC, p1)
				}
				if tc.rc == trap && !strings.Contains(before, "trapped") {
					t.Errorf("the user's trap is not there before the request: %q", p1)
				}
				if b, _ := os.ReadFile(filepath.Join(dir, "run", "esc")); tc.esc && len(b) != 0 {
					t.Errorf("$AISH_RUN/esc left %q", b)
				}
			})
		}
	}
}
