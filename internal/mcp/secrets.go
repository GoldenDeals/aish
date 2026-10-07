package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// commandTimeout bounds a command of env_command or headers_command: pass
// may wait for a passphrase with nobody there to type it. A variable for
// the tests.
var commandTimeout = 30 * time.Second

// checkCommands refuses a key given both a literal and a command, of
// which one would be lost without a word, and an empty command. A header
// is one key whatever its case, as HTTP has it.
func (s Server) checkCommands() error {
	for _, m := range []struct {
		name     string
		lit, cmd map[string]string
		key      func(string) string
	}{
		{"env", s.Env, s.EnvCommand, func(k string) string { return k }},
		{"headers", s.Headers, s.HeadersCommand, http.CanonicalHeaderKey},
	} {
		lit := map[string]bool{}
		for k := range m.lit {
			lit[m.key(k)] = true
		}
		for _, k := range slices.Sorted(maps.Keys(m.cmd)) {
			if lit[m.key(k)] {
				return fmt.Errorf("%s is in both %s and %s_command", k, m.name, m.name)
			}
			if strings.TrimSpace(m.cmd[k]) == "" {
				return fmt.Errorf("%s_command %s: empty command", m.name, k)
			}
		}
	}
	return nil
}

// resolve gives the environment and the headers of s: a literal with its
// ${VAR} expanded from the proxy's environment; a command, its output.
// The commands run at every start of the server, nothing is cached: a
// token may have changed since the last one. The error of a command has
// the values of s masked, those of the commands before it too.
func resolve(ctx context.Context, s Server) (env, headers map[string]string, err error) {
	expand := func(lit map[string]string) map[string]string {
		out := make(map[string]string, len(lit))
		for k, v := range lit {
			out[k] = os.ExpandEnv(v)
		}
		return out
	}
	env, headers = expand(s.Env), expand(s.Headers)
	var got []string
	for _, c := range []struct {
		name string
		cmd  map[string]string
		dst  map[string]string
	}{
		{"env_command", s.EnvCommand, env},
		{"headers_command", s.HeadersCommand, headers},
	} {
		for _, k := range slices.Sorted(maps.Keys(c.cmd)) {
			v, err := output(ctx, c.cmd[k])
			if err == nil && c.name == "headers_command" && strings.ContainsAny(v, "\r\n") {
				err = errors.New("more than one line")
			}
			if err != nil {
				return nil, nil, fmt.Errorf("%s %s: %s", c.name, k, maskError(err.Error(), s, got))
			}
			c.dst[k] = v
			got = append(got, v)
		}
	}
	return env, headers, nil
}

// output runs command with sh in the home directory, with the proxy's
// environment and no input, and gives what it printed without the newline
// that ends it. The command runs in a process group of its own, killed
// whole at the timeout: a pipeline or a $(…) leaves children behind
// otherwise.
func output(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir, _ = os.UserHomeDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// A child that outlives the shell and holds its stdout, a daemon the
	// command started, say, is not waited for.
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	stderr := &tail{}
	cmd.Stdout, cmd.Stderr = &out, stderr
	err := cmd.Run()
	switch {
	case errors.Is(err, exec.ErrWaitDelay):
		err = nil
	case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = errors.New("timed out")
	case err != nil && ctx.Err() != nil:
		err = ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("%v%s", err, stderr)
	}
	v := out.String()
	if s, ok := strings.CutSuffix(v, "\n"); ok {
		v = strings.TrimSuffix(s, "\r")
	}
	if v == "" {
		return "", fmt.Errorf("no output%s", stderr)
	}
	return v, nil
}
