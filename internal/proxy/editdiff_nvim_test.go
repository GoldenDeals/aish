package proxy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/capture"
)

// nvimEnv is a neovim of its own: no config, data or state of the user's,
// $AISH_RUN the test's, nothing else of the environment but PATH.
func nvimEnv(t *testing.T, run, tmp string) []string {
	t.Helper()
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("no nvim in PATH")
	}
	home := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "AISH_RUN=" + run, "TMPDIR=" + tmp}
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		env = append(env, v+"="+filepath.Join(home, strings.ToLower(v)))
	}
	return env
}

// runNvim runs headless neovim with the plugin in dir. setup is the Lua
// table the user's config calls its setup with, "" for none: before the
// plugin's own file is sourced, as init.lua does, or, with late, after it,
// as lazy.nvim does with opts.
func runNvim(t *testing.T, env []string, dir, setup string, late bool, args ...string) {
	t.Helper()
	plugin, err := filepath.Abs("../../contrib/nvim")
	if err != nil {
		t.Fatal(err)
	}
	full := []string{"--headless", "-u", "NONE", "-i", "NONE", "-n", "--cmd", "set rtp^=" + plugin}
	source := []string{"--cmd", "source " + filepath.Join(plugin, "plugin", "aish.lua")}
	if late {
		full = append(full, source...)
	}
	if setup != "" {
		full = append(full, "--cmd", "lua require('aish').setup("+setup+")")
	}
	if !late {
		full = append(full, source...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvim", append(full, args...)...)
	cmd.Dir, cmd.Env = dir, env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nvim: %v\n%s", err, out)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// workDir is a directory to edit in, by its real path: the plugin names
// files so.
func workDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Two files edited and saved in neovim: their diff goes to the journal
// with the command, after its full-screen line.
func TestNvimEditsTwoFiles(t *testing.T) {
	p, dir := editsProxy(t)
	env := nvimEnv(t, p.run, t.TempDir())
	work := workDir(t)
	writeFiles(t, work, map[string]string{"a": "one\ntwo\nthree\n", "b": "alpha\nbeta\n"})

	p.marker(Marker{Kind: "cmd-start", Payload: "nvim a b"})
	runNvim(t, env, work, "{ ignore = {} }", false, "a", "b",
		"-c", "%s/two/TWO/", "-c", "w", "-c", "next", "-c", "call append(0, 'zero')", "-c", "wq")
	p.output([]byte(fullScreenOut))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + work})

	a, b := filepath.Join(work, "a"), filepath.Join(work, "b")
	want := capture.FullScreen + "\n" + editsNote + "\n" +
		"--- " + a + "\n+++ " + a + "\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n" +
		"--- " + b + "\n+++ " + b + "\n@@ -1,2 +1,3 @@\n+zero\n alpha\n beta"
	if e := lastShell(t, p); e.Output != want || e.TUI {
		t.Errorf("recorded %q, TUI %v; want %q", e.Output, e.TUI, want)
	}
	if left := leftDiffs(t, dir); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// A new file, a deleted one, a change not saved; Ctrl+Z, which tells what
// was saved before it, `fg` getting the rest.
func TestNvimEditsNewDeletedSuspended(t *testing.T) {
	p, dir := editsProxy(t)
	env := nvimEnv(t, p.run, t.TempDir())
	work := workDir(t)
	writeFiles(t, work, map[string]string{"a": "one\ntwo\n", "d": "gone\n"})

	runNvim(t, env, work, "{ ignore = {} }", true, "a", "d",
		"-c", "s/one/ONE/ | w | doautocmd VimSuspend",
		"-c", "$s/two/TWO/ | w | next | call delete(expand('%:p'))",
		"-c", "e c | call setline(1, ['new']) | w",
		"-c", "normal ox", "-c", "qa!")

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, e.Name()+":"+string(b))
	}
	slices.SortFunc(texts, func(x, y string) int { // by the number after the pid
		return strings.Compare(x[strings.IndexByte(x, '-'):], y[strings.IndexByte(y, '-'):])
	})
	a, c, d := filepath.Join(work, "a"), filepath.Join(work, "c"), filepath.Join(work, "d")
	want := []string{
		"--- " + a + "\n+++ " + a + "\n@@ -1,2 +1,2 @@\n-one\n+ONE\n two\n",
		"--- " + a + "\n+++ " + a + "\n@@ -1,2 +1,2 @@\n ONE\n-two\n+TWO\n" +
			"--- /dev/null\n+++ " + c + "\n@@ -0,0 +1 @@\n+new\n" +
			"--- " + d + "\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n",
	}
	if len(texts) != len(want) {
		t.Fatalf("diffs %q, want %q", texts, want)
	}
	for i, w := range want {
		if _, got, _ := strings.Cut(texts[i], ":"); got != w {
			t.Errorf("diff %d: %q, want %q", i+1, got, w)
		}
	}
}

// Outside aish, and in a temporary directory by default, nothing is told.
func TestNvimEditsQuiet(t *testing.T) {
	run := t.TempDir()
	work := workDir(t)
	writeFiles(t, work, map[string]string{"a": "one\n"})

	env := nvimEnv(t, run, filepath.Dir(work)) // work is in $TMPDIR
	runNvim(t, env, work, "", false, "a", "-c", "s/one/ONE/", "-c", "wq")
	if _, err := os.Stat(filepath.Join(run, editsDir)); !os.IsNotExist(err) {
		t.Errorf("a file in $TMPDIR told of: %v", err)
	}

	env = slices.DeleteFunc(env, func(v string) bool { return strings.HasPrefix(v, "AISH_RUN=") })
	runNvim(t, env, work, "{ ignore = {} }", false, "a", "-c", "s/ONE/one/", "-c", "wq")
	if _, err := os.Stat(filepath.Join(run, editsDir)); !os.IsNotExist(err) {
		t.Errorf("told outside aish: %v", err)
	}
}
