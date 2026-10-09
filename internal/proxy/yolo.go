package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
)

// `aish yolo` turns the checks of the agent's calls off, `aish yolo off`
// on again: internal/agent/yolo.go tells what goes and what stays. The
// switch is the proxy's, for the life of the shell: no session keeps it,
// so aish resume, clear and new leave it as it is, and a new aish, with
// --resume too, starts without it. As with the model, only the user at the
// shell's foreground switches it: a command of the agent's would turn its
// own checks off, and the agent's bash runs in the same shell.
//
// The checks go off only once the user said Yes to the proxy's question, a
// firm one (confirm.go): the foreground is a barrier, not a boundary, and
// so is the guard, which denies the agent's line by its text. Code a line
// of the agent's leaves to the shell, a trap, PROMPT_COMMAND, runs at the
// prompt in the foreground, with the request over, and the text that calls
// rpc yolo need not say aish yolo. The answer comes from the keys of the
// terminal, which no process in the shell can type: what it writes goes to
// the PTY. aish yolo off asks nothing.

var (
	errYoloAsks     = errors.New("yolo is switched by the user, not by the assistant")
	errYoloDeclined = errors.New("yolo not confirmed: the checks stay on")
)

// yoloWait is how long the question of aish yolo waits for the answer. A
// variable: the tests shorten it.
var yoloWait = time.Minute

// yoloQuestion is asked before yolo goes on: what goes and what stays.
const yoloQuestion = "\x1b[0m" + bold + "aish yolo" + "\x1b[0m" + ": till this shell exits, the assistant's calls go " +
	"without the policies, the [policy] rules, the questions and the limits of the subagents' bash; " +
	"the guard and the hooks stay.\r\n" + bold + "Turn the checks off?" + "\x1b[0m"

// setYolo turns yolo on, once the user confirmed it, or off.
func (p *Proxy) setYolo(ctx context.Context, on bool) error {
	fg := p.fromShell(ctx)
	p.mu.Lock()
	if p.asking {
		p.mu.Unlock()
		return errYoloAsks
	}
	if fg != nil {
		p.mu.Unlock()
		return fg
	}
	if !on || p.yolo {
		p.yolo = on
		p.mu.Unlock()
		return nil
	}
	q := yoloQuestion
	if p.col.off {
		q = "\r\n" + q // after the output of the line that called it
	}
	p.mu.Unlock()

	// The time stands while the Ctrl+O viewer covers the question, as with
	// the agent's: see askClock.
	wait := agent.WithAnswerTime(ctx, yoloWait)
	yes, err := p.confirm(wait, q)
	switch {
	case ctx.Err() != nil:
		// The caller is gone, Ctrl+C: it can tell nobody the checks went off.
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		_, why, _ := agent.AnswerTime(wait)
		return fmt.Errorf("yolo not confirmed: %w; the checks stay on", why)
	case err != nil:
		return fmt.Errorf("yolo not confirmed: %w; the checks stay on", err)
	case !yes:
		return errYoloDeclined
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asking {
		return errYoloAsks // a request began meanwhile: not the user's to answer for
	}
	p.yolo = true
	return nil
}

// yoloOn is Agent.Yolo: the agent and its subagents ask it at each call,
// from goroutines of their own and never under p.mu, which it takes.
func (p *Proxy) yoloOn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.yolo
}

// The mark that ends the prompt's status while yolo is on, in a color of
// its own: the status's may be as dim as can be.
const (
	yoloMark  = "yolo"
	yoloColor = "\x1b[0;31m"
)

// yoloStatus is the status text with the mark while yolo is on; of no
// text, the mark alone, which drawStatus draws when the status is off or
// does not fit. Called under p.mu.
func (p *Proxy) yoloStatus(text string) string {
	switch {
	case !p.yolo:
		return text
	case text == "":
		return yoloMark
	}
	return text + " · " + yoloMark
}

// paintYolo colors the mark of the status l draws; the width l keeps is
// of the plain text. Called under p.mu.
func (p *Proxy) paintYolo(l *inputLine) {
	if base, ok := strings.CutSuffix(l.text, yoloMark); ok && p.yolo {
		l.text = base + yoloColor + yoloMark
	}
}
