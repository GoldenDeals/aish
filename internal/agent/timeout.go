package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// A call runs for a time at most. The model gives it as the argument
// timeout, in seconds, which aish adds to the schema of every tool but
// ask_user; without it tool_timeout of the config holds. Past it the call
// is stopped as Esc stops it (interrupt.go): what it printed is its
// result, "[timed out after 2m]" after it, and the request goes on. A
// command for the shell runs after the request has returned, so its timer
// is the agent's (handLimit), and the shell stops it (Interrupter) with
// exit 124, as timeout(1) does.
//
// The argument is aish's, not the tool's: neither the tool, nor the policy
// and the hooks see it, so it changes no verdict. A tool with a timeout
// argument of its own (task_wait, an MCP or external tool) keeps it as it
// is: aish adds none and sets its calls no limit, not even the default —
// one shorter than the tool's would cut a call the model gave longer. An
// MCP call ends at its server's timeout still, whichever comes first.
// task works as long as its subagents do unless the model limits it: they
// are made to work for minutes, and each of their own calls has its limit.

// timeoutArg is the name of aish's argument.
const timeoutArg = "timeout"

// overTime is the interruption of a call that ran for lim.
func overTime(lim time.Duration) *Interruption {
	return &Interruption{Why: "timed out after " + span(lim), Code: 124}
}

// ownTimeout tells whether schema, a tool's, has a timeout argument.
func ownTimeout(schema map[string]any) bool {
	props, _ := schema["properties"].(map[string]any)
	_, ok := props[timeoutArg]
	return ok
}

// schema is t's schema as the model gets it: with aish's timeout argument,
// unless t has one of its own or is ask_user, whose form waits on a timer
// of its own (ask_timeout).
func (a *Agent) schema(t tools.Tool) map[string]any {
	s := t.Schema()
	if tools.IsDialog(t) || ownTimeout(s) {
		return s
	}
	def := "default " + limitText(a.Cfg.ToolLimit()) + "; 0 for none"
	if _, ok := t.(*taskTool); ok {
		def = "default none"
	}
	desc := "Seconds the call may run before aish stops it, its output so far being its result (" + def + ")"
	if handsOff(t) {
		desc = "Seconds the command may run before aish stops it, with exit 124 and its output so far (" + def + ")"
	}
	props, _ := s["properties"].(map[string]any)
	props = maps.Clone(props)
	if props == nil {
		props = map[string]any{}
	}
	props[timeoutArg] = map[string]any{"type": "number", "description": desc}
	if s = maps.Clone(s); s == nil {
		s = map[string]any{"type": "object"}
	}
	s["properties"] = props
	return s
}

// limitText is a limit as the schema tells it: in seconds, as the model
// gives it.
func limitText(d time.Duration) string {
	if d <= 0 {
		return "none"
	}
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// callLimit is how long the call of t with args may run, 0 for no limit,
// and args without aish's timeout argument, which the tool does not take.
func (a *Agent) callLimit(t tools.Tool, args map[string]any) (time.Duration, map[string]any, error) {
	if tools.IsDialog(t) || ownTimeout(t.Schema()) {
		return 0, args, nil
	}
	v, given := args[timeoutArg]
	if given {
		args = maps.Clone(args)
		delete(args, timeoutArg)
	}
	if given && v != nil {
		d, err := seconds(v)
		if err != nil {
			return 0, args, fmt.Errorf("%s: %w", timeoutArg, err)
		}
		return d, args, nil
	}
	if _, ok := t.(*taskTool); ok {
		return 0, args, nil
	}
	return a.Cfg.ToolLimit(), args, nil
}

// seconds reads the timeout argument: a number of seconds, as the schema
// says, or that number or a duration (90s, 5m) as text, which models send
// too. 0 is no limit, and so is a number too big for a duration.
func seconds(v any) (time.Duration, error) {
	bad := fmt.Errorf("%v is not a number of seconds", v)
	var s float64
	switch v := v.(type) {
	case float64:
		s = v
	case string:
		text := strings.TrimSpace(v)
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			d, err := time.ParseDuration(text)
			if err != nil || d < 0 {
				return 0, bad
			}
			return d, nil
		}
		s = f
	default:
		return 0, bad
	}
	switch {
	case math.IsNaN(s) || s < 0:
		return 0, bad
	case s >= math.MaxInt64/float64(time.Second):
		return 0, nil
	}
	d := time.Duration(s * float64(time.Second))
	if s > 0 && d < time.Millisecond {
		d = time.Millisecond // a limit still, not none
	}
	return d, nil
}

// limit is ctx with the limit lim, for a call: past it the call is
// stopped, interrupted (interrupt.go) for overTime.
func limit(ctx context.Context, lim time.Duration) (context.Context, context.CancelFunc) {
	if lim <= 0 {
		return ctx, func() {}
	}
	ctx = context.WithValue(ctx, limitKey{}, lim)
	return context.WithTimeoutCause(ctx, lim, overTime(lim))
}

// limitKey keys the limit of a call in its context: task gives it to the
// subagents it leaves in the background, whose contexts are their own.
type limitKey struct{}

// givenLimit is the limit of the call ctx is of, 0 for none.
func givenLimit(ctx context.Context) time.Duration {
	lim, _ := ctx.Value(limitKey{}).(time.Duration)
	return lim
}

// bgContext is the context of a subagent in the background: its own, not
// the request's, which ends first, with lim, the limit the call of task
// gave it, 0 for none.
func bgContext(lim time.Duration) (context.Context, context.CancelFunc) {
	if lim > 0 {
		return context.WithTimeoutCause(context.Background(), lim, overTime(lim))
	}
	return context.WithCancel(context.Background())
}

// callArgs are the arguments of call c as its tool takes them: the
// model's, without aish's timeout; nil when they do not decode.
func (a *Agent) callArgs(c session.ToolCall) map[string]any {
	args, err := tools.Decode(c.Args)
	if err != nil {
		return nil
	}
	if t, ok := a.tool(c.Name); ok {
		_, args, _ = a.callLimit(t, args)
	}
	return args
}

// handLimit is the timer of the command handed to the shell, which runs it
// once the request has returned. It has a lock of its own: the timer fires
// on a goroutine of its own.
type handLimit struct {
	mu    sync.Mutex
	id    string // the call whose command it stops, "" for none
	timer *time.Timer
}

// limitHanded has the shell stop the command it was handed as call id once
// lim is over; 0 is no limit. A shell that cannot stop a command
// (Interrupter) runs it with none.
func (a *Agent) limitHanded(id string, lim time.Duration) {
	a.unlimitHanded()
	sh, ok := a.Shell.(Interrupter)
	if !ok || lim <= 0 {
		return
	}
	in := overTime(lim)
	h := &a.handLimit
	h.mu.Lock()
	defer h.mu.Unlock()
	h.id = id
	h.timer = time.AfterFunc(lim, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.id == id {
			sh.Interrupt(id, in)
		}
	})
}

// unlimitHanded drops the timer of the command handed off: its output is
// back (Resume), or its request is over (Start). Once it has returned, the
// timer stops nothing.
func (a *Agent) unlimitHanded() {
	h := &a.handLimit
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
	}
	h.id, h.timer = "", nil
}

// stoppedBy is the interruption ctx ended with, nil if it has not ended or
// for another cause.
func stoppedBy(ctx context.Context) *Interruption {
	var in *Interruption
	if ctx.Err() != nil && errors.As(context.Cause(ctx), &in) {
		return in
	}
	return nil
}

// Interrupt stops the command of a subagent run as call id, for in: the
// timer of its limit does (limitHanded). One not started yet does not run
// (run).
func (s *subShell) Interrupt(id string, in *Interruption) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id {
		return false
	}
	if s.stop != nil {
		s.stop(in)
	} else if s.early == nil {
		s.early = in
	}
	return true
}

// run runs f, the command handed off as call id, in a context Interrupt
// cancels; dir is where it runs. A command stopped before it started does
// not start: its output is the interruption's alone.
func (s *subShell) run(ctx context.Context, id, dir string, f func(context.Context) rpc.Output) rpc.Output {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s.mu.Lock()
	early := s.early
	if s.id == id && early == nil {
		s.stop = cancel
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.id, s.stop, s.early = "", nil, nil
		s.mu.Unlock()
	}()
	if early != nil {
		return rpc.Output{Exit: early.Code, Cwd: dir, Why: early.Why}
	}
	return f(ctx)
}

var _ Interrupter = (*subShell)(nil)
