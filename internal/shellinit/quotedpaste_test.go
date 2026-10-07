package shellinit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestQuotedPaste types Ctrl+V or Ctrl+Q and then a bracketed paste at
// the prompt of a bash with the rc aish gives it, the user's ~/.bashrc and
// ~/.inputrc first. The paste runs as pasted, not as ^[[200~text~, and so
// it does when it comes after keyseq-timeout, as Ctrl+Shift+V does a
// second after the Ctrl+V of habit. Ctrl+V before Tab, Esc or an arrow
// key still inserts the character. A binding of the user's under Ctrl+V,
// of Ctrl+V itself or of the \C-x\C-q aish sends it to stays.
func TestQuotedPaste(t *testing.T) {
	const (
		paste = "\x16\x1b[200~echo v >>out\x1b[201~\r"
		chars = `v='a` + "\x16\tb" + `'; printf '%s\n' "${v@Q}" >>chars` + "\r" +
			`v='a` + "\x16\x1bb" + `'; printf '%s\n' "${v@Q}" >>chars` + "\r" +
			`v='a` + "\x16\x1b[Ab" + `'; printf '%s\n' "${v@Q}" >>chars` + "\r"
		quoted = "$'a\\tb'\n$'a\\Eb'\n$'a\\E[Ab'\n"
	)
	all := []string{`emacs \C-q`, `emacs \C-v`, `vi-insert \C-v`}
	cases := []struct {
		name, inputrc, bashrc string
		keys                  []string          // typed after the shell wrote ready, the first at once
		want                  map[string]string // file the keys wrote: its contents
		bound                 []string          // keymap and key sent to \C-x\C-q
	}{
		{
			name:  "emacs",
			keys:  []string{paste + "\x11\x1b[200~echo q >>out\x1b[201~\r" + chars},
			want:  map[string]string{"out": "v\nq\n", "chars": quoted},
			bound: all,
		},
		{
			name:   "vi",
			bashrc: "set -o vi\n",
			keys:   []string{paste + chars},
			want:   map[string]string{"out": "v\n", "chars": quoted},
			bound:  all,
		},
		{
			// The terminal pastes without the brackets then, and the
			// keys go as they did.
			name:    "no bracketed paste",
			inputrc: "set enable-bracketed-paste off\n",
			keys:    []string{paste + "\x16echo x >>out\r" + chars},
			want:    map[string]string{"out": "v\nx\n", "chars": quoted},
			bound:   all,
		},
		{
			name:    "paste after keyseq-timeout",
			inputrc: "set keyseq-timeout 20\n",
			keys:    []string{": >ready\r\x16", "\x1b[200~echo v >>out\x1b[201~\r"},
			want:    map[string]string{"out": "v\n"},
			bound:   all,
		},
		{
			name:   "user's paste binding",
			bashrc: `bind '"\C-v\e[200~": "v="'` + "\n",
			keys:   []string{"\x16\x1b[200~x\r" + `printf %s "$v" >user` + "\r"},
			want:   map[string]string{"user": "x"},
			bound:  []string{`emacs \C-q`, `vi-insert \C-v`},
		},
		{
			name:    "user's Ctrl+V",
			inputrc: `"\C-v": "V"` + "\n",
			keys:    []string{"v=\x16x; " + `printf %s "$v" >user` + "\r"},
			want:    map[string]string{"user": "Vx"},
			bound:   []string{`emacs \C-q`, `vi-insert \C-v`},
		},
		{
			name:   "user's sequence under Ctrl+V",
			bashrc: `bind -m vi-insert -x '"\C-va": :'` + "\n",
			bound:  []string{`emacs \C-q`, `emacs \C-v`},
		},
		{
			name:   "user's C-x C-q",
			bashrc: `bind '"\C-x\C-q": kill-line'` + "\n",
			bound:  []string{`vi-insert \C-v`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keys := append([]string{}, c.keys...)
			if len(keys) == 0 {
				keys = []string{""}
			}
			keys[len(keys)-1] += "{ bind -m emacs -p; bind -m emacs -s; } >emacs; { bind -m vi-insert -p; bind -m vi-insert -s; } >vi-insert\r"
			dir, out := typed(t, c.inputrc, c.bashrc, keys...)
			if strings.Contains(out, "bind:") {
				t.Errorf("bind complained:\n%s", out)
			}
			for name, want := range c.want {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != want {
					t.Errorf("%s: %q (%v), want %q\n%s", name, got, err, want, out)
				}
			}
			var bound []string
			for _, km := range []string{"emacs", "vi-insert"} {
				b, err := os.ReadFile(filepath.Join(dir, km))
				if err != nil {
					t.Fatal(err)
				}
				dump := string(b)
				for _, m := range regexp.MustCompile(`(?m)^"(\\C-.)": "\\C-x\\C-q"$`).FindAllStringSubmatch(dump, -1) {
					bound = append(bound, km+" "+m[1])
					for _, l := range []string{`"\C-x\C-q": quoted-insert`, `"\C-x\C-q\e[200~": bracketed-paste-begin`} {
						if !strings.Contains(dump, "\n"+l+"\n") {
							t.Errorf("%s: no %s", km, l)
						}
					}
				}
			}
			if strings.Join(bound, ", ") != strings.Join(c.bound, ", ") {
				t.Errorf("sent to \\C-x\\C-q: %s, want %s", strings.Join(bound, ", "), strings.Join(c.bound, ", "))
			}
		})
	}
}

// typed starts bash in a home of its own with the user's bashrc and
// inputrc, sources the rc file aish gives it and types keys at its prompt:
// the first chunk at once, each next one a while after the shell created
// the file ready, for readline to be waiting for it by then. It returns
// the directory the shell ran in, for what the keys wrote there, and all
// the shell printed.
func typed(t *testing.T, inputrc, bashrc string, keys ...string) (string, string) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Sourced at the prompt, not by --rcfile: that one reads the system's
	// bashrc too.
	keys = append([]string{"source " + filepath.Join(dir, "rc") + "\n" + keys[0]}, keys[1:]...)
	cmd := exec.CommandContext(ctx, "bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = cleanEnv("PS1=> ", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "TERM=xterm",
		"HOME="+dir, "INPUTRC="+filepath.Join(dir, ".inputrc"), "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"))
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = r
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	err = write(w, filepath.Join(dir, "ready"), keys)
	w.Close()
	if werr := cmd.Wait(); err == nil {
		err = werr
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return dir, out.String()
}

func write(w *os.File, ready string, keys []string) error {
	for i, k := range keys {
		if i > 0 {
			for n := 0; ; n++ {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if n == 200 {
					return fmt.Errorf("no %s", ready)
				}
				time.Sleep(25 * time.Millisecond)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if _, err := w.WriteString(k); err != nil {
			return err
		}
	}
	return nil
}
