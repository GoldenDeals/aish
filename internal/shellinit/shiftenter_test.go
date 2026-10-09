package shellinit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shiftEnter is what the proxy gives the shell for Shift+Enter at the
// prompt (newlineKeys in internal/proxy/shiftenter.go): Ctrl+V Ctrl+J.
const shiftEnter = "\x16\n"

// TestShiftEnter types Shift+Enter as the proxy gives it to bash in each
// keymap of its: in emacs and vi insert mode the newline goes in the
// request, Enter sends it whole; in vi command mode the newline goes in the
// line at the cursor, and nothing is sent or run.
func TestShiftEnter(t *testing.T) {
	const bashrc = `AISH_BIN=fake
fake() { [[ $2 == start ]] && printf '%s\x1f' "$4" >>sent; }
`
	for _, c := range []struct {
		name, bashrc string
		keys         []string
		sent         string
	}{
		{"emacs", "", []string{"Say a" + shiftEnter + "next\r"}, "Say a\nnext\x1f"},
		{"vi insert", "set -o vi\n", []string{"Say a" + shiftEnter + "next\r"}, "Say a\nnext\x1f"},
		// Esc takes the cursor back onto the a.
		{"vi command", "set -o vi\n", []string{": >ready\rSay a\x1b", shiftEnter + "i\r"}, "Say \na\x1f"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, out := typed(t, "", c.bashrc+bashrc, c.keys...)
			if b, _ := os.ReadFile(filepath.Join(dir, "sent")); string(b) != c.sent {
				t.Errorf("sent %q, want %q\n%q", b, c.sent, out)
			}
		})
	}
}

// TestZshShiftEnter is TestShiftEnter for zsh, whose vi command mode binds
// no Ctrl+V: there Shift+Enter is Enter, as without aish.
func TestZshShiftEnter(t *testing.T) {
	const zshrc = `AISH_BIN=fake
fake() { [[ $2 == start ]] && print -rn -- "$4"$'\x1f' >>$HOME/sent }
`
	for _, c := range []struct {
		name, zshrc string
		steps       []zstep
		sent        string
	}{
		{"emacs", "", []zstep{{prompts: 1, keys: "Say a" + shiftEnter + "next\r"}}, "Say a\nnext\x1f"},
		{"vi insert", "bindkey -v\n", []zstep{{prompts: 1, keys: "Say a" + shiftEnter + "next\r"}}, "Say a\nnext\x1f"},
		{"vi command", "bindkey -v\n", []zstep{{prompts: 1, keys: "Say a\x1b"}, {delay: 300 * time.Millisecond, keys: shiftEnter}}, "Say a\x1f"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, out := zshTyped(t, c.zshrc+zshrc, append(c.steps, zstep{prompts: 2})...)
			if b, _ := os.ReadFile(filepath.Join(dir, "sent")); string(b) != c.sent {
				t.Errorf("sent %q, want %q\n%q", b, c.sent, out)
			}
		})
	}
}
