package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// fallbackVar names the shell aish execs in its place when it could not
// start one under it: its value is that shell's pid. The rc line of README
// skips aish in the shell with this pid, as AISH_SOCK skips it in the one
// under aish. Not in its children: a tmux started there would have no aish
// in any pane.
const fallbackVar = "AISH_FALLBACK"

// fallback keeps a terminal open when aish, run in place of the user's
// shell (exec aish in ~/.bashrc, or the command of the terminal), cannot
// start: on its own exit the terminal would close at once, its error with
// it.
type fallback struct {
	// pid is $AISH_FALLBACK as aish was started with: the pid of the shell
	// aish fell back to. It is aish's own when that shell ran it again by
	// exec: its rc file with an aish line of before AISH_FALLBACK, and
	// falling back to the same shell would loop. It is aish's parent's
	// when aish was typed there, to try again: that shell waits for aish,
	// and one more under it at each try would pile up.
	pid int
}

// takeFallback reads $AISH_FALLBACK and takes it out of aish's environment:
// a shell aish starts, under it or in its place, is not the one it names.
func takeFallback() fallback {
	pid, _ := strconv.Atoi(os.Getenv(fallbackVar))
	os.Unsetenv(fallbackVar)
	return fallback{pid: pid}
}

// startsProxy tells whether args start the shell under aish, aish becoming
// the proxy, rather than run a subcommand.
func startsProxy(args []string) bool {
	return len(args) == 0 || strings.HasPrefix(args[0], "-")
}

// fail tells err, which kept aish from starting the shell under it, and
// execs a shell in aish's place: shell, the one config.toml names ("" if
// it was not read), or $SHELL. It returns only when it does not, with the
// code aish exits with otherwise: inside aish, where the proxy holds the
// terminal, under the shell aish fell back to, and with no terminal on
// stdin, where no one types.
func (f fallback) fail(shell string, err error) int {
	code := fail(err)
	if os.Getenv("AISH_SOCK") != "" || f.pid != 0 && f.pid == os.Getppid() ||
		!term.IsTerminal(int(os.Stdin.Fd())) {
		return code
	}
	for _, c := range f.shells(shell) {
		fmt.Fprintf(os.Stderr, "\x1b[2maish: %s\x1b[0m\n", c.note)
		err := syscall.Exec(c.path, c.argv, c.env)
		fmt.Fprintf(os.Stderr, "aish: %s: %v\n", c.path, err)
	}
	return code
}

// shellExec is a shell to exec in aish's place.
type shellExec struct {
	path string
	argv []string
	env  []string
	note string // what the user gets, under the error
}

// shells are the shells to exec in aish's place, the first that execs:
// shell, $SHELL, bash, sh, but not aish itself ($SHELL after chsh aish).
// Interactive, with their rc files; without them when this shell ran aish
// again, which it would do once more.
func (f fallback) shells(shell string) []shellExec {
	self, _ := os.Executable()
	again := f.pid == os.Getpid()
	env := append(os.Environ(), fallbackVar+"="+strconv.Itoa(os.Getpid()))
	var list []shellExec
	seen := map[string]bool{}
	for _, name := range []string{shell, os.Getenv("SHELL"), "bash", "sh"} {
		if name == "" {
			continue
		}
		path, err := exec.LookPath(name)
		if err != nil || seen[path] || sameFile(path, self) {
			continue
		}
		seen[path] = true
		base := filepath.Base(path)
		c := shellExec{path: path, argv: []string{base, "-i"}, env: env}
		c.note = base + " starts in place of aish; fix that and run aish"
		if again {
			// The flags that keep each shell from reading the rc files;
			// sh reads none but $ENV.
			switch {
			case strings.HasPrefix(base, "bash"):
				c.argv = []string{base, "--norc", "-i"}
			case strings.HasPrefix(base, "zsh"):
				c.argv = []string{base, "-f", "-i"}
			case base == "sh":
				c.env = without(env, "ENV")
			default:
				continue
			}
			c.note = "aish was run again by the shell it fell back to; " + base +
				" starts without the rc files (the aish line of README skips that shell)"
		}
		list = append(list, c)
	}
	return list
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	return err == nil && os.SameFile(ia, ib)
}

// without is env with no variable name.
func without(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}
