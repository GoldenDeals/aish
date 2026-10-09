package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// `aish apply-config` and `aish trust` inside aish change what the agent's
// calls are checked by and what runs with them: the policies, the [policy]
// rules, the endpoint of the requests, the hooks and tools of a project.
// As with `aish yolo` (yolo.go), nothing changes until the user said Yes
// to the proxy's firm question (confirm.go), every time. The agent writes
// the files as the user does, config.toml and the policies too, and may
// leave the command for the shell to run at its prompt, a trap,
// PROMPT_COMMAND, a line of ~/.bashrc: with the request over and in the
// foreground, past p.asking and fromShell, and past the guard, which reads
// the text of the agent's lines. Those refuse the agent's command and a
// process in the background before anything is asked; the keyboard, which
// the proxy reads and no process in the shell can type at, is what tells
// the user's command from the rest.

// userWait is how long the questions of aish apply-config and aish trust
// wait for the answer. A variable: the tests shorten it.
var userWait = time.Minute

// userConfirms asks q, a firm question, and is nil once the user answered
// Yes. Else the error ends with no, what stays as it was. The time stands
// while the Ctrl+O viewer covers the question, as with the agent's: see
// askClock.
func (p *Proxy) userConfirms(ctx context.Context, q, no string) error {
	p.mu.Lock()
	if p.col.off {
		q = "\r\n" + q // after the output of the line that called it
	}
	p.mu.Unlock()
	wait := agent.WithAnswerTime(ctx, userWait)
	yes, err := p.confirm(wait, q)
	switch {
	case ctx.Err() != nil:
		// The caller is gone, Ctrl+C: it could tell nobody what was done.
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		_, why, _ := agent.AnswerTime(wait)
		return fmt.Errorf("not confirmed: %w; %s", why, no)
	case err != nil:
		return fmt.Errorf("not confirmed: %w; %s", err, no)
	case !yes:
		return fmt.Errorf("not confirmed; %s", no)
	}
	return nil
}

// applyQuestion is asked before what aish apply-config read and checked
// goes in force: the keys of config.toml and the files that changed, as
// res has them, and what waits for a restart. With nothing changed it is
// asked all the same: the Yes is not to depend on how well the changes
// were told.
func applyQuestion(res rpc.Applied) string {
	var lines []string
	if len(res.Keys) > 0 {
		lines = append(lines, "config.toml: "+strings.Join(res.Keys, ", "))
	}
	for _, f := range res.Files {
		lines = append(lines, shortPath(f))
	}
	q := "\x1b[0m" + bold + "aish apply-config" + "\x1b[0m"
	if len(lines) == 0 {
		q += ": nothing changed since the config was read"
	} else {
		q += " puts in force the config as it is now:\r\n  " + strings.Join(lines, "\r\n  ")
	}
	if len(res.Restart) > 0 {
		names := make([]string, len(res.Restart))
		for i, k := range res.Restart {
			names[i] = shortPath(k)
		}
		q += "\r\n  (only a restart of aish applies " + strings.Join(names, ", ") + ")"
	}
	return q + "\r\n" + bold + "Apply?" + "\x1b[0m"
}

// trustQuestion is asked before aish trust trusts the project file at
// path: keys are what it sets that runs code from the repository.
func trustQuestion(path string, keys []string) string {
	q := "\x1b[0m" + bold + "aish trust" + "\x1b[0m " + shortPath(path)
	if len(keys) == 0 {
		q += ": it sets nothing that runs code"
	} else {
		q += ": these keys run code from the repository, its hooks and tools as they are now:\r\n  " +
			strings.Join(keys, "\r\n  ")
	}
	return q + "\r\n" + bold + "Trust it?" + "\x1b[0m"
}

// shortPath is path with the home directory as ~.
func shortPath(path string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "/" {
		if rest, ok := strings.CutPrefix(path, h+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
