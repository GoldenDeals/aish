package shellinit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestViCommandEnter types Enter in vi command mode, after Esc, at a bash
// with the rc aish gives it. A request goes to the assistant, and nothing
// of it runs; a command runs. A request ending in a backslash goes on to
// the next line, in insert mode, as a command's does, and so does one
// held in insert mode and sent from command mode. The user's set -e, -u
// or -k changes none of it.
func TestViCommandEnter(t *testing.T) {
	const bashrc = `set -o vi
AISH_BIN=fake AISH_RUN=$HOME
printf 'N\n' >"$AISH_RUN/nonce"
fake() { [[ $2 == start ]] && printf '%s\x1f' "$4" >>sent; }
`
	for _, c := range []struct{ name, opts string }{{"none", ":"}, {"set -euk", "set -euk"}} {
		t.Run(c.name, func(t *testing.T) {
			dir, out := typed(t, "", bashrc+c.opts+"\n",
				": >ready\rWhy does `: >ran` fail\x1b", "\r",
				"ls >listed\x1b", "\r",
				"Say a \\\x1b", "\r", "next\r",
				"Say b \\\rmore\x1b", "\r",
				"Say c\x1b", "\n",
				"echo alive >>out\r",
			)
			read := func(name string) string {
				b, _ := os.ReadFile(filepath.Join(dir, name))
				return string(b)
			}
			want := "Why does `: >ran` fail\x1fSay a\nnext\x1fSay b\nmore\x1fSay c\x1f"
			if got := read("sent"); got != want {
				t.Errorf("sent %q, want %q\n%q", got, want, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
				t.Errorf("the request ran:\n%q", out)
			}
			if !strings.Contains(read("listed"), "ready") {
				t.Errorf("ls did not run:\n%q", out)
			}
			if read("out") != "alive\n" {
				t.Errorf("the shell is gone:\n%q", out)
			}
		})
	}
}

// TestViCommandUserEnter checks that a key the user bound in vi command
// mode, Enter itself or a sequence under it, stays his there, while
// another, untouched, goes through the route. Where none does, aish binds
// nothing there, a request held by a backslash included.
func TestViCommandUserEnter(t *testing.T) {
	const macro = `"\C-x\C-a\C-x\C-b"`
	for _, c := range []struct {
		name, bashrc string
		bound        []string // keys bound to Enter's macro in vi command mode
	}{
		{"none", "", []string{`\C-j`, `\C-m`}},
		{"function", `bind -m vi-command '"\C-m": kill-whole-line'`, []string{`\C-j`}},
		{"bind -x", `bind -m vi-command -x '"\C-j": :'`, []string{`\C-m`}},
		{"macro", `bind -m vi-command '"\C-m": "x"'`, []string{`\C-j`}},
		{"sequence under", `bind -m vi-command '"\C-j\C-j": kill-line'`, []string{`\C-m`}},
		{"both", `bind -m vi-command '"\C-m": kill-whole-line'; bind -m vi-command '"\C-j": kill-whole-line'`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Ctrl+X D dumps the keymap while the request is held.
			dump := `bind -m vi-insert -x '"\C-xd": { bind -m vi-command -p; bind -m vi-command -s; bind -m vi-command -X; } >dump'`
			dir, out := typed(t, "", "set -o vi\n"+c.bashrc+"\n"+dump+"\n", "Say a \\\r\x18d\x15\r")
			if strings.Contains(out, "bind:") {
				t.Errorf("bind complained:\n%s", out)
			}
			b, err := os.ReadFile(filepath.Join(dir, "dump"))
			if err != nil {
				t.Fatalf("%v\n%q", err, out)
			}
			got := string(b)
			var bound []string
			for _, m := range regexp.MustCompile(`(?m)^"(\\C-.)": `+regexp.QuoteMeta(macro)+`$`).FindAllStringSubmatch(got, -1) {
				bound = append(bound, m[1])
			}
			if strings.Join(bound, " ") != strings.Join(c.bound, " ") {
				t.Errorf("bound %q, want %q\n%s", bound, c.bound, got)
			}
			for _, l := range []string{`"\C-x\C-a" "__aish_route"`, `"\C-x\C-b": vi-append-eol`} {
				if has := strings.Contains(got, "\n"+l+"\n"); has != (len(c.bound) > 0) {
					t.Errorf("%s in vi command mode: %v, want %v\n%s", l, has, !has, got)
				}
			}
			if len(c.bound) == 0 && strings.Contains(got, `"\C-x`) {
				t.Errorf("aish bound \\C-x in vi command mode:\n%s", got)
			}
		})
	}

	// Enter stays the user's: the line goes, unsent; Ctrl+J is aish's.
	dir, out := typed(t, "", "set -o vi\nbind -m vi-command '\"\\C-m\": kill-whole-line'\n"+
		"AISH_BIN=fake\nfake() { [[ $2 == start ]] && printf '%s\\x1f' \"$4\" >>sent; }\n",
		": >ready\rSay a\x1b", "\r", "iecho ok >>out\rSay b\x1b", "\n")
	b, _ := os.ReadFile(filepath.Join(dir, "sent"))
	if got, want := string(b), "Say b\x1f"; got != want {
		t.Errorf("sent %q, want %q\n%q", got, want, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out")); string(b) != "ok\n" {
		t.Errorf("out %q, want %q\n%q", b, "ok\n", out)
	}
}

// TestZshViCommandEnter is TestViCommandEnter for zsh, where Enter in vi
// command mode is the accept-line widget aish wraps already.
func TestZshViCommandEnter(t *testing.T) {
	const zshrc = `bindkey -v
AISH_BIN=fake
fake() { [[ $2 == start ]] && print -rn -- "$4"$'\x1f' >>$HOME/sent }
`
	pause := 300 * time.Millisecond
	dir, printed := zshTyped(t, zshrc,
		zstep{prompts: 1, keys: "Why does `: >ran` fail\x1b"}, zstep{delay: pause, keys: "\r"},
		zstep{prompts: 2, keys: "ls >listed\x1b"}, zstep{delay: pause, keys: "\r"},
		zstep{prompts: 3},
	)
	if b, _ := os.ReadFile(filepath.Join(dir, "sent")); string(b) != "Why does `: >ran` fail\x1f" {
		t.Errorf("sent %q\n%q", b, printed)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Errorf("the request ran:\n%q", printed)
	}
	if _, err := os.Stat(filepath.Join(dir, "listed")); err != nil {
		t.Errorf("ls did not run:\n%q", printed)
	}
}
