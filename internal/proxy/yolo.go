package proxy

import (
	"context"
	"errors"
	"strings"
)

// `aish yolo` turns the checks of the agent's calls off, `aish yolo off`
// on again: internal/agent/yolo.go tells what goes and what stays. The
// switch is the proxy's, for the life of the shell: no session keeps it,
// so aish resume, clear and new leave it as it is, and a new aish, with
// --resume too, starts without it. As with the model, only the user at the
// shell's foreground switches it: a command of the agent's would turn its
// own checks off, and the agent's bash runs in the same shell.

var errYoloAsks = errors.New("yolo is switched by the user, not by the assistant")

// setYolo turns yolo on or off.
func (p *Proxy) setYolo(ctx context.Context, on bool) error {
	fg := p.fromShell(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asking {
		return errYoloAsks
	}
	if fg != nil {
		return fg
	}
	p.yolo = on
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
