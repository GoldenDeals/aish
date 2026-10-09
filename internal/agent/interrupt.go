package agent

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/GoldenDeals/aish/internal/session"
)

// A call can be stopped alone, the request going on without it: Esc in the
// proxy stops the call in progress (Interrupt). Each call has a context of
// its own, made from the request's, and stopping it cancels that context
// with an *Interruption for the cause: the call's result is what it printed
// so far with the interruption's Why after it, and the model takes its
// turn. A bash command runs in the shell after the request has returned:
// the shell stops it (Interrupter), and the output Resume gets carries Why
// (rpc.Output.Why).

// Interruption is why a call stopped before its end while the request went
// on: the cause its context is cancelled with. Another stop than Esc, a
// time limit, is an Interruption of its own.
type Interruption struct {
	Why  string // for the model, in brackets after what the call printed
	Code int    // the exit status a command so stopped ends with
}

func (in *Interruption) Error() string { return in.Why }

// ByUser is the interruption of Esc.
var ByUser = &Interruption{Why: "interrupted by the user", Code: 130}

// Interrupter is a Shell that can stop the command it runs as call id
// before the command's end, for in: the command ends with in.Code, and the
// output Wait returns for it carries in.Why. It is false when the shell
// runs no such command, or is done with it.
type Interrupter interface {
	Interrupt(id string, in *Interruption) bool
}

// inCall is the call in progress, for Interrupt to stop. It has a lock of
// its own: Interrupt comes from the keys, not from the request.
type inCall struct {
	mu   sync.Mutex
	stop context.CancelCauseFunc // nil between calls
}

// Interrupt stops the call in progress for in, the request going on
// without it. It is false when there is none: the request is in a turn of
// the model or between steps, and stopping it is stopping the request.
func (a *Agent) Interrupt(in *Interruption) bool {
	a.inCall.mu.Lock()
	defer a.inCall.mu.Unlock()
	if a.inCall.stop == nil {
		return false
	}
	a.inCall.stop(in)
	return true
}

// callContext is the context of a call made in req, which Interrupt
// cancels until done.
func (a *Agent) callContext(req context.Context) (ctx context.Context, done func()) {
	ctx, cancel := context.WithCancelCause(req)
	a.inCall.mu.Lock()
	a.inCall.stop = cancel
	a.inCall.mu.Unlock()
	return ctx, func() {
		a.inCall.mu.Lock()
		a.inCall.stop = nil
		a.inCall.mu.Unlock()
		cancel(nil)
	}
}

// interrupted is the interruption that stopped call, the context of a call
// made in req; nil when none did: the call ran, or the request is over
// (Ctrl+C), which leaves the call pending.
func interrupted(req, call context.Context) *Interruption {
	if req.Err() != nil || call.Err() == nil {
		return nil
	}
	var in *Interruption
	if errors.As(context.Cause(call), &in) {
		return in
	}
	return nil
}

// cutShort is the result of a call in stopped: what it printed, then
// in's Why in brackets.
func cutShort(out string, in *Interruption) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return "[" + in.Why + "]"
	}
	return out + "\n[" + in.Why + "]"
}

// settle is what call c, made in req with the context ctx, returns when it
// returned err: a call ctx was interrupted for gets a result, unless it has
// one already, and the request goes on.
func (a *Agent) settle(req, ctx context.Context, c session.ToolCall, err error) error {
	in := interrupted(req, ctx)
	if err == nil || in == nil {
		return err
	}
	for _, p := range pending(a.entries) {
		if p.ID == c.ID {
			return a.append(toolResult(c, cutShort("", in), true))
		}
	}
	return nil
}
