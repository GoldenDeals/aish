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

// The tests here need zle, which zsh runs only on a terminal: zsh gets one
// of its own, a PTY.

// zstep is keys typed once the shell has printed prompts primary prompts
// (cmd-end) and file exists in its home, if one is named, and delay later.
type zstep struct {
	prompts int
	file    string
	delay   time.Duration
	keys    string
}

// zshTyped starts zsh as aish does, with zshrc for the user's .zshrc, in
// a terminal 80 columns wide, types the steps and then exit. AISH_BIN
// writes what it is called with to called in the home. It returns the
// home, where the shell runs, and all it printed.
func zshTyped(t *testing.T, zshrc string, steps ...zstep) (string, string) {
	t.Helper()
	zsh := zshPath(t)
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	stub := filepath.Join(dir, "aish-stub")
	files := map[string]string{
		filepath.Join(run, "zsh", ".zshenv"): ZshEnv,
		filepath.Join(run, "zsh", ".zshrc"):  ZshRC(),
		filepath.Join(run, "nonce"):          "N0NCE\n",
		filepath.Join(run, "route"):          "",
		filepath.Join(run, "next.cmd"):       "",
		filepath.Join(dir, ".zshrc"):         "PS1='> '\n" + zshrc,
		stub:                                 "#!/bin/sh\necho \"$@\" >>\"$HOME/called\"\n",
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(zsh, "-i")
	cmd.Dir = dir
	cmd.Env = cleanEnv("TERM=xterm", "LC_ALL=C.UTF-8", "HOME="+dir, "ZDOTDIR="+filepath.Join(run, "zsh"),
		"HISTFILE="+filepath.Join(dir, ".hist"), "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"),
		"AISH_RUN="+run, "AISH_BIN="+stub)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
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
	for _, s := range append(steps, zstep{keys: "exit\r"}) {
		for {
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				t.Fatalf("waiting for %d prompts and %q before %q:\n%q", s.prompts, s.file, s.keys, printed())
			}
			_, err := os.Stat(filepath.Join(dir, s.file))
			if strings.Count(printed(), ";cmd-end;") >= s.prompts && (s.file == "" || err == nil) {
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
		t.Fatalf("zsh did not exit:\n%q", printed())
	}
	f.Close()
	<-read
	return dir, printed()
}

// TestZshExpandInterrupt interrupts a request in its slow $(...) as Ctrl+C
// does: the request is not sent, neither expanded nor as typed, and what
// follows the substitution in it does not run, whether the user traps
// SIGINT or not. The line stays in history, $? is 130, and the user's trap
// is back.
func TestZshExpandInterrupt(t *testing.T) {
	const req = "Wait $(: >started; sleep 30) and $(: >after) done"
	for _, tc := range []struct{ name, zshrc, trap string }{
		{"no trap", "", ""},
		{"user trap", "TRAPINT() { print -r -- user; return $((128 + $1)) }\n", "TRAPINT () {\n\tprint -r -- user\n\treturn $((128 + $1))\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			dir, printed := zshTyped(t, tc.zshrc,
				zstep{prompts: 1, keys: "Home $HOME here\r"},
				zstep{prompts: 2, keys: req + "\r"},
				zstep{file: "started", keys: "\x03"},
				zstep{prompts: 3, keys: `{ print -r -- "rc=$?"; fc -ln -3; functions TRAPINT } >out 2>&1` + "\r"},
				zstep{prompts: 4},
			)
			if d := time.Since(start); d > 15*time.Second {
				t.Errorf("took %v: the substitution was not interrupted", d)
			}
			b, _ := os.ReadFile(filepath.Join(dir, "called"))
			if want := "agent start -- Home " + dir + " here\n"; string(b) != want {
				t.Errorf("sent %q, want only %q", b, want)
			}
			if _, err := os.Stat(filepath.Join(dir, "after")); err == nil {
				t.Error("the rest of the request ran after Ctrl+C")
			}
			o, _ := os.ReadFile(filepath.Join(dir, "out"))
			want := "rc=130\nHome $HOME here\n" + req + "\n" + tc.trap
			if !strings.HasPrefix(string(o), want) || strings.Contains(string(o), "__aish") {
				t.Errorf("after Ctrl+C:\n%s\nwant\n%s", o, want)
			}
			codes := regexp.MustCompile(";cmd-end;([0-9]+);").FindAllStringSubmatch(printed, -1)
			if len(codes) < 3 || codes[2][1] != "130" {
				t.Errorf("cmd-end codes %q, want 130 for the request", codes)
			}
		})
	}
}

// TestZshQuotedPaste types Ctrl+V and then a bracketed paste: it runs as
// pasted, not as ^[[200~text~, and so it does when it comes a second after
// the Ctrl+V of habit. Ctrl+V before Tab, Esc, an arrow key or a letter
// still inserts the character. Ctrl+Q is zsh's push-line in emacs, and
// stays so. A binding of the user's under Ctrl+V or of Ctrl+V itself, or a
// quoted-insert of his own, stays.
func TestZshQuotedPaste(t *testing.T) {
	const (
		paste = "\x16\x1b[200~echo v >>out\x1b[201~\r"
		chars = "v='a\x16\tb'; print -r -- ${(q+)v} >>chars\r" +
			"v='a\x16\x1bb'; print -r -- ${(q+)v} >>chars\r" +
			"v='a\x16\x1b[Ab'; print -r -- ${(q+)v} >>chars\r" +
			"v='a\x16яb'; print -r -- ${(q+)v} >>chars\r"
		quoted = "$'a\\tb'\n$'a\\C-[b'\n$'a\\C-[[Ab'\naяb\n"
		dump   = "{ bindkey -M emacs '^V'; bindkey -M emacs '^Q'; bindkey -M viins '^V'; bindkey -M viins '^Q' } >bound\r"
		ours   = "\"^V\" __aish_quote\n\"^Q\" push-line\n\"^V\" __aish_vi_quote\n\"^Q\" __aish_vi_quote\n"
	)
	for _, tc := range []struct {
		name, zshrc string
		steps       []zstep
		want        map[string]string
		bound       string
	}{
		{
			name:  "emacs",
			steps: []zstep{{prompts: 1, keys: paste}, {prompts: 2, keys: "\x11\x1b[200~echo q >>out\x1b[201~\r"}, {prompts: 3, keys: chars}},
			want:  map[string]string{"out": "v\nq\n", "chars": quoted},
			bound: ours,
		},
		{
			name:  "vi",
			zshrc: "bindkey -v\n",
			steps: []zstep{{prompts: 1, keys: paste}, {prompts: 2, keys: chars}},
			want:  map[string]string{"out": "v\n", "chars": quoted},
			bound: ours,
		},
		{
			name:  "paste a second after",
			steps: []zstep{{prompts: 1, keys: "\x16"}, {delay: time.Second, keys: "\x1b[200~echo late >>out\x1b[201~\r"}},
			want:  map[string]string{"out": "late\n"},
		},
		{
			name:  "user's bindings",
			zshrc: "bindkey -M emacs '^V' backward-char\nbindkey -M viins '^Va' vi-add-eol\nmyq() { zle .quoted-insert }\nzle -N vi-quoted-insert myq\n",
			bound: "\"^V\" backward-char\n\"^Q\" push-line\n\"^V\" vi-quoted-insert\n\"^Q\" vi-quoted-insert\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := 1
			for _, s := range tc.steps {
				lines += strings.Count(s.keys, "\r")
			}
			steps := append(tc.steps, zstep{prompts: lines, keys: dump}, zstep{file: "bound"})
			dir, printed := zshTyped(t, tc.zshrc, steps...)
			for name, want := range tc.want {
				if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
					t.Errorf("%s: %q (%v), want %q\n%q", name, got, err, want, printed)
				}
			}
			if tc.bound != "" {
				if got, _ := os.ReadFile(filepath.Join(dir, "bound")); string(got) != tc.bound {
					t.Errorf("bound:\n%s\nwant\n%s", got, tc.bound)
				}
			}
		})
	}
}

// TestZshUnechoRows: the echo of a request zle leaves is the prompt and
// the line it was rewritten to, one row down from it after Enter, and
// zle takes the cursor to the next row after a full one: a line just as
// wide as the terminal is two rows up.
func TestZshUnechoRows(t *testing.T) {
	cases := []struct {
		prompt string
		up     string
	}{
		{strings.Repeat("x", 32) + "% ", "\x1b[1A"}, // 58 columns of 60 with the line
		{strings.Repeat("x", 34) + "% ", "\x1b[2A"}, // 60
		{strings.Repeat("x", 35) + "% ", "\x1b[2A"}, // 61
		{strings.Repeat("x", 93) + "% ", "\x1b[2A"}, // 119
		{strings.Repeat("x", 94) + "% ", "\x1b[3A"}, // 120
		{strings.Repeat("ж", 34) + "% ", "\x1b[2A"}, // 60, more in bytes
	}
	var script strings.Builder
	for _, c := range cases {
		script.WriteString("COLUMNS=60 __aish_unecho_draw text " + quote(c.prompt) + "; print -n $'\\x1f'\n")
	}
	got, _ := zshRun(t, "", "", script.String())
	for i, c := range cases {
		if !strings.HasPrefix(at(got, i), c.up+"\r") {
			t.Errorf("prompt of %d columns: %q, want it up %q", len([]rune(c.prompt)), at(got, i), c.up)
		}
	}
}
