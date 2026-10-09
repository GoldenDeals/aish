package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The rc lines of README: the one that skips the shell aish falls back to,
// and the one from before it.
const (
	rcLine    = `[[ -z ${AISH_SOCK-} && ${AISH_FALLBACK-} != $$ && $- == *i* ]] && command -v aish >/dev/null && exec aish`
	rcLineOld = `[[ -z ${AISH_SOCK-} && $- == *i* ]] && command -v aish >/dev/null && exec aish`
)

// TestFallbackHelper is aish for the terminals of the fallback tests, run
// by the stub they put in PATH: not a test of its own.
func TestFallbackHelper(t *testing.T) {
	if os.Getenv("AISH_TEST_FALLBACK") != "1" {
		return
	}
	args := os.Args
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	os.Exit(run(args))
}

// fallbackHome is a home for a terminal whose aish cannot start: its
// config.toml is broken. aish is the stub in bin, this test binary, which
// adds a line to runs each time it starts.
type fallbackHome struct {
	dir, conf, runs, stub string
}

func newFallbackHome(t *testing.T, rcFiles map[string]string) fallbackHome {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	h := fallbackHome{
		dir:  dir,
		conf: filepath.Join(dir, "config.toml"),
		runs: filepath.Join(dir, "runs"),
		stub: filepath.Join(dir, "bin", "aish"),
	}
	files := map[string]string{
		// Broken as TOML and as YAML.
		h.conf: "{[\n",
		h.stub: "#!/bin/sh\necho run >>" + strconv.Quote(h.runs) + "\n" +
			"AISH_TEST_FALLBACK=1 exec " + strconv.Quote(bin) + " -test.run='^TestFallbackHelper$' -- \"$@\"\n",
	}
	for name, text := range rcFiles {
		files[filepath.Join(dir, name)] = text
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// env is the whole environment of a terminal of h, as one with nothing of
// the aish the tests may run under, with shell for $SHELL.
func (h fallbackHome) env(shell string, extra ...string) []string {
	return append([]string{
		"HOME=" + h.dir,
		"PATH=" + filepath.Dir(h.stub) + ":" + filepath.Dir(shell) + ":/usr/bin:/bin",
		"SHELL=" + shell,
		"TERM=dumb",
		"TMUX=/nonexistent,1,0",
		"HISTFILE=" + filepath.Join(h.dir, ".hist"),
		"AISH_CONFIG=" + h.conf,
	}, extra...)
}

// started is how many times aish started.
func (h fallbackHome) started(t *testing.T) int {
	b, err := os.ReadFile(h.runs)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "run\n")
}

// terminal is a process in a PTY, as a terminal runs its command, and all
// it printed.
type terminal struct {
	t    *testing.T
	f    *os.File
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	read chan struct{}
	done chan error
}

func startTerminal(t *testing.T, env []string, argv ...string) *terminal {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = filepath.Dir(argv[0])
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 300})
	if err != nil {
		t.Fatal(err)
	}
	term := &terminal{t: t, f: f, cmd: cmd, read: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		defer close(term.read)
		b := make([]byte, 4096)
		for {
			n, err := f.Read(b)
			term.mu.Lock()
			term.out.Write(b[:n])
			term.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() { term.done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		f.Close()
	})
	return term
}

func (term *terminal) printed() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.out.String()
}

// wait waits for re in what the terminal printed and returns its match.
func (term *terminal) wait(re string) []string {
	term.t.Helper()
	r := regexp.MustCompile(re)
	deadline := time.Now().Add(20 * time.Second)
	for ended := false; ; {
		if m := r.FindStringSubmatch(term.printed()); m != nil {
			return m
		}
		if ended || time.Now().After(deadline) {
			term.t.Fatalf("waiting for %s:\n%q", re, term.printed())
		}
		select {
		case <-term.read:
			ended = true // all it printed is read: one more look
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (term *terminal) typed(keys string) {
	term.t.Helper()
	if _, err := io.WriteString(term.f, keys); err != nil {
		term.t.Fatal(err)
	}
}

// exited waits for the process to end and returns its exit code.
func (term *terminal) exited() int {
	term.t.Helper()
	select {
	case err := <-term.done:
		<-term.read
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if err != nil {
			term.t.Fatal(err)
		}
		return 0
	case <-time.After(20 * time.Second):
		term.t.Fatalf("still running:\n%q", term.printed())
	}
	return -1
}

// TestFallbackBash: aish as a terminal runs it, with config.toml broken,
// prints the error and execs $SHELL in its place, with the user's rc
// files. The rc line of README skips aish there, so aish runs once: the
// error stands above the shell's prompt, the terminal stays. aish typed
// there fails as before, with no shell under it.
func TestFallbackBash(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	h := newFallbackHome(t, map[string]string{".bashrc": "PS1='fallback> '\n" + rcLine + "\n"})
	term := startTerminal(t, h.env(bash), h.stub)
	term.wait(`fallback> $`)
	out := term.printed()
	errAt := strings.Index(out, "aish: "+h.conf+": ")
	noteAt := strings.Index(out, "bash starts in place of aish")
	if errAt < 0 || noteAt < errAt || strings.LastIndex(out, "fallback> ") < noteAt {
		t.Fatalf("want the error, the note, then the prompt:\n%q", out)
	}
	if n := h.started(t); n != 1 {
		t.Errorf("aish started %d times, want once:\n%q", n, out)
	}
	term.typed("aish\n")
	again := term.wait(`(?s)fallback> (.*)fallback> $`)[1]
	if !strings.Contains(again, "aish: "+h.conf+": ") || strings.Contains(again, "in place of aish") {
		t.Errorf("aish typed in the shell: want the error alone:\n%q", again)
	}
	term.typed("echo \"[$AISH_FALLBACK:$$:$-]\"; exit 3\n")
	m := term.wait(`\[(\d+):(\d+):(\w+)\]`)
	if m[1] != m[2] || !strings.Contains(m[3], "i") {
		t.Errorf("AISH_FALLBACK %s, the shell %s, flags %s: want the shell's pid, interactive", m[1], m[2], m[3])
	}
	if code := term.exited(); code != 3 {
		t.Errorf("exit code %d, want the shell's 3", code)
	}
}

// TestFallbackZsh: the same with zsh, config.toml unread: $SHELL.
func TestFallbackZsh(t *testing.T) {
	t.Parallel()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	h := newFallbackHome(t, map[string]string{".zshrc": "PS1='zfallback> '\n" + rcLine + "\n"})
	term := startTerminal(t, h.env(zsh), h.stub)
	term.wait(`zfallback> `)
	if out := term.printed(); !strings.Contains(out, "aish: "+h.conf+": ") || !strings.Contains(out, "zsh starts in place of aish") {
		t.Fatalf("want the error and the note:\n%q", out)
	}
	term.typed("echo \"[$AISH_FALLBACK:$$]\"; exit\n")
	m := term.wait(`\[(\d+):(\d+)\]`)
	if m[1] != m[2] {
		t.Errorf("AISH_FALLBACK %s, the shell %s: want the shell's pid", m[1], m[2])
	}
	term.exited()
	if n := h.started(t); n != 1 {
		t.Errorf("aish started %d times, want once:\n%q", n, term.printed())
	}
}

// TestFallbackOldLine: the rc line of before AISH_FALLBACK runs aish again
// in the shell aish fell back to, by exec, so with the same pid. That aish
// does not fall back the same way, which would loop: it execs bash
// without the rc files, and the terminal stays.
func TestFallbackOldLine(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	h := newFallbackHome(t, map[string]string{".bashrc": "PS1='full> '\n" + rcLineOld + "\n"})
	term := startTerminal(t, h.env(bash), h.stub)
	term.wait(`bash starts without the rc files`)
	term.wait(`bash-[0-9.]+\$ $`)
	term.typed("echo \"bare:$((6*7))\"; exit\n")
	term.wait(`bare:42`)
	term.exited()
	if n := h.started(t); n != 2 {
		t.Errorf("aish started %d times, want twice:\n%q", n, term.printed())
	}
	if out := term.printed(); strings.Contains(out, "full> ") {
		t.Errorf("the rc file was read again:\n%q", out)
	}
}

// TestNoFallback: a subcommand, aish inside aish and aish with no terminal
// on stdin exit with 1 on the same error, as before; no shell starts.
func TestNoFallback(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	h := newFallbackHome(t, map[string]string{".bashrc": "PS1='fallback> '\n"})
	for _, c := range []struct {
		name string
		env  []string
		args []string
	}{
		{"subcommand", nil, []string{"status"}},
		{"inside aish", []string{"AISH_SOCK=" + filepath.Join(h.dir, "sock")}, nil},
	} {
		term := startTerminal(t, h.env(bash, c.env...), append([]string{h.stub}, c.args...)...)
		if code := term.exited(); code != 1 {
			t.Errorf("%s: exit code %d, want 1:\n%q", c.name, code, term.printed())
		}
		if out := term.printed(); !strings.Contains(out, "aish: "+h.conf+": ") || strings.Contains(out, "in place of aish") {
			t.Errorf("%s: want the error alone:\n%q", c.name, out)
		}
	}
	cmd := exec.Command(h.stub)
	cmd.Env = h.env(bash)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || strings.Contains(string(out), "in place of aish") {
		t.Errorf("no terminal: %v, want exit code 1 and the error alone:\n%s", err, out)
	}
}

// TestFallbackLineDocumented: README, install.sh and the package give the
// rc line the tests run.
func TestFallbackLineDocumented(t *testing.T) {
	for _, name := range []string{"README.md", "install.sh", "packaging/arch/aish-git.install"} {
		b, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), rcLine) {
			t.Errorf("%s: no %s", name, rcLine)
		}
	}
}

// TestFallbackShells: the shell named in config.toml first, then $SHELL,
// bash, sh, aish itself never; run again by the shell it fell back to,
// aish execs those it can keep from their rc files, without them.
func TestFallbackShells(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"fish", "zsh", "bash", "sh"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ENV", "/some/rc")
	t.Setenv("SHELL", self)
	argvs := func(list []shellExec) string {
		var s []string
		for _, c := range list {
			s = append(s, strings.Join(c.argv, " "))
		}
		return strings.Join(s, ", ")
	}
	if got, want := argvs(fallback{}.shells("fish")), "fish -i, bash -i, sh -i"; got != want {
		t.Errorf("shells: %s, want %s", got, want)
	}
	t.Setenv("SHELL", filepath.Join(dir, "zsh"))
	if got, want := argvs(fallback{}.shells("")), "zsh -i, bash -i, sh -i"; got != want {
		t.Errorf("shells: %s, want %s", got, want)
	}
	again := fallback{pid: os.Getpid()}.shells("fish")
	if got, want := argvs(again), "zsh -f -i, bash --norc -i, sh -i"; got != want {
		t.Errorf("shells run again: %s, want %s", got, want)
	}
	for _, c := range again {
		mark := fallbackVar + "=" + strconv.Itoa(os.Getpid())
		if !slices.Contains(c.env, mark) {
			t.Errorf("%s: no %s", c.argv[0], mark)
		}
		if slices.Contains(c.env, "ENV=/some/rc") != (c.argv[0] != "sh") {
			t.Errorf("%s: ENV in its environment: %v", c.argv[0], slices.Contains(c.env, "ENV=/some/rc"))
		}
	}
}
