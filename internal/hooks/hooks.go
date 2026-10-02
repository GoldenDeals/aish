// Package hooks lets the user step into the agent's work with plain
// executables, as Claude Code's hooks do: one in <hooks_dir>/<event>/ runs
// at each such event of a request, reads what happened as JSON on stdin
// and may answer with JSON on stdout: add context to a request, turn a
// tool call down, replace a tool's result. The package finds the hooks,
// runs one and reads its answer; what the answer does to the request is
// the agent's business (internal/agent/hooks.go). Hooks run in the proxy,
// with the directory and the environment of the user's shell.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/inebotov/aish/internal/tools"
)

// The events, each a directory of hooks.
const (
	// UserPrompt runs on a request before it goes to the model.
	UserPrompt = "user-prompt"
	// PreTool runs on a tool call the policy has not denied.
	PreTool = "pre-tool"
	// PostTool runs on a tool's result before it is recorded.
	PostTool = "post-tool"
	// Stop runs when the request is over.
	Stop = "stop"
)

// Events are the events in the order a request meets them.
var Events = []string{UserPrompt, PreTool, PostTool, Stop}

// Timeout is how long a hook may run: one that hangs must not hang the
// request.
const Timeout = 5 * time.Second

// timeout is Timeout, shorter in tests.
var timeout = Timeout

const (
	// maxReply bounds what is read of a hook's stdout: a post-tool reply
	// holds a whole result.
	maxReply = 4 << 20
	// maxStderr bounds what is kept of its stderr, the reason of a deny.
	maxStderr = 4 << 10
)

// replyKeys are the keys a reply to each event may have. Any other is a
// mistake, which must not pass without a word, as an unknown key of the
// config does not. A stop hook answers nothing, so what it prints is not
// read.
var replyKeys = map[string][]string{
	UserPrompt: {"context", "deny"},
	PreTool:    {"action", "reason", "args"},
	PostTool:   {"output"},
}

// Hook is one executable for an event.
type Hook struct {
	Event string
	Name  string // the file's name
	Path  string
}

// String names the hook in messages.
func (h Hook) String() string { return h.Event + "/" + h.Name }

// Set is the hooks found for a request, by event, in the order they run.
// The nil Set has none.
type Set struct {
	byEvent map[string][]Hook
}

// For is the hooks of event.
func (s *Set) For(event string) []Hook {
	if s == nil {
		return nil
	}
	return s.byEvent[event]
}

// Find reads the hooks in dirs, a directory or a list of them in the form
// of PATH: those of the first directory run first, each event's in the
// order of their names. A missing directory has none. Problems are what
// was skipped and is likely a mistake, a directory named after no event
// or a file that is not executable: a guard that silently does not run
// is worse than a line on every request.
func Find(dirs string) (*Set, []error) {
	s := &Set{byEvent: map[string][]Hook{}}
	var problems []error
	for _, dir := range filepath.SplitList(dirs) {
		if dir == "" {
			continue
		}
		top, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, err)
			}
			continue
		}
		for _, d := range top {
			if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				continue // a file here is the hooks' own, a library say
			}
			if !slices.Contains(Events, d.Name()) {
				problems = append(problems, fmt.Errorf("%s: no such event (want one of %s)",
					filepath.Join(dir, d.Name()), strings.Join(Events, ", ")))
				continue
			}
			hs, errs := scan(filepath.Join(dir, d.Name()), d.Name())
			s.byEvent[d.Name()] = append(s.byEvent[d.Name()], hs...)
			problems = append(problems, errs...)
		}
	}
	return s, problems
}

// scan reads the hooks of one event's directory, sorted by name.
func scan(dir, event string) ([]Hook, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{err}
	}
	var hs []Hook
	var problems []error
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
			continue
		}
		path := filepath.Join(dir, name)
		st, err := os.Stat(path) // a symlink counts as what it points to
		switch {
		case err != nil:
			problems = append(problems, err)
		case st.IsDir():
		case st.Mode()&0o111 == 0:
			problems = append(problems, fmt.Errorf("%s: not executable, skipped", path))
		default:
			hs = append(hs, Hook{Event: event, Name: name, Path: path})
		}
	}
	return hs, problems
}

// Reply is a hook's answer. Pointers tell a key given as "" from one not
// given: {"output": ""} empties the result.
type Reply struct {
	// user-prompt: text to add to the request; Deny turns it down.
	Context string  `json:"context"`
	Deny    *string `json:"deny"`
	// pre-tool: allow, deny or ask, with the reason; Args replace the
	// call's arguments.
	Action string         `json:"action"`
	Reason string         `json:"reason"`
	Args   map[string]any `json:"args"`
	// post-tool: the result to record instead.
	Output *string `json:"output"`
}

// Result is how a run went. Err means the hook gave no answer: it could
// not start, ran out of time, was stopped or printed something that is
// not a reply. Otherwise Exit is its exit status, 128+n when killed by
// signal n, as the shell has it, and Reply is read when Exit is 0.
type Result struct {
	Hook   Hook
	Exit   int
	Stderr string
	Reply  Reply
	Err    error
}

// Run runs the hook in ex, the shell's directory and environment, with in
// (a value that marshals to a JSON object) and the event's name under
// "event" on its stdin. It gets Timeout; past it, or once ctx is done, its
// process group is killed, so that a child it left holding its stdout
// does not hold the request either.
func (h Hook) Run(ctx context.Context, ex tools.Exec, in any) Result {
	r := Result{Hook: h, Exit: -1}
	stdin, err := input(h.Event, in)
	if err != nil {
		r.Err = err
		return r
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, h.Path)
	cmd.Dir = ex.Dir
	cmd.Env = ex.Env
	cmd.Stdin = bytes.NewReader(stdin)
	out, errOut := &capped{max: maxReply}, &capped{max: maxStderr}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	r.Stderr = strings.TrimSpace(errOut.String())
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		r.Err = ctx.Err()
		return r
	case err == nil:
	case errors.Is(err, exec.ErrWaitDelay):
		// It exited 0, but what it started keeps its stdout open (a
		// notifier run with &): what it printed before counts.
	case tctx.Err() != nil:
		r.Err = fmt.Errorf("timed out after %v", timeout)
		return r
	case errors.As(err, &exit):
		r.Exit = exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			r.Exit = 128 + int(ws.Signal())
		}
		return r
	default:
		r.Err = err
		return r
	}
	r.Exit = 0
	if out.over {
		r.Err = fmt.Errorf("reply longer than %d bytes", maxReply)
		return r
	}
	r.Reply, r.Err = parse(h.Event, out.Bytes())
	return r
}

// input is in as JSON, with the event's name added, on a line of its own:
// `read` in a shell wants the newline.
func input(event string, in any) ([]byte, error) {
	obj := map[string]json.RawMessage{}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("input: %w", err)
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return nil, fmt.Errorf("input is not an object: %w", err)
		}
	}
	obj["event"], _ = json.Marshal(event)
	b, err := json.Marshal(obj)
	return append(b, '\n'), err
}

// parse reads a reply to event: a JSON object, or nothing at all.
func parse(event string, out []byte) (Reply, error) {
	var r Reply
	out = bytes.TrimSpace(out)
	keys, answers := replyKeys[event]
	if len(out) == 0 || !answers {
		return r, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		return r, fmt.Errorf("the reply is not a JSON object: %q", head(out))
	}
	for k := range obj {
		if !slices.Contains(keys, k) {
			return r, fmt.Errorf("unknown key %q in the reply (a %s hook answers with %s)", k, event, strings.Join(keys, ", "))
		}
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return Reply{}, fmt.Errorf("the reply: %w", err)
	}
	switch r.Action {
	case "", "allow", "deny", "ask":
	default:
		return Reply{}, fmt.Errorf("action %q: want \"allow\", \"deny\" or \"ask\"", r.Action)
	}
	return r, nil
}

// head is the start of b, enough to see what a hook printed instead.
func head(b []byte) string {
	if len(b) > 80 {
		return string(b[:80]) + "…"
	}
	return string(b)
}

// capped keeps the first max bytes written to it and drops the rest, so
// that a hook printing without end neither blocks nor fills the memory.
type capped struct {
	bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); len(p) > room {
		c.over = true
		c.Buffer.Write(p[:max(room, 0)])
	} else {
		c.Buffer.Write(p)
	}
	return len(p), nil
}
