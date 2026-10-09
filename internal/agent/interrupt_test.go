package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/tools"
)

// waitTool runs in the proxy, as an MCP tool does: it says it runs, and
// returns what it has once its context ends.
type waitTool struct{ running chan struct{} }

func (w *waitTool) Name() string           { return "wait" }
func (w *waitTool) Desc() string           { return "Waits" }
func (w *waitTool) Args() []tools.Arg      { return nil }
func (w *waitTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (w *waitTool) Execute(ctx context.Context, _ tools.Exec, _ map[string]any, _ io.Writer) (string, error) {
	close(w.running)
	<-ctx.Done()
	return "partial", ctx.Err()
}

// startInterrupted runs a request whose first call is name and, once
// running is closed, stops that call as Esc does. It returns whether
// Interrupt found the call and what Start returned.
func startInterrupted(t *testing.T, a *Agent, cwd string, running <-chan struct{}) (bool, error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- a.Start(context.Background(), "go", tools.Exec{Dir: cwd}) }()
	select {
	case <-running:
	case err := <-done:
		t.Fatalf("the request ended before the call ran: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the call did not run")
	}
	stopped := a.Interrupt(ByUser)
	select {
	case err := <-done:
		return stopped, err
	case <-time.After(10 * time.Second):
		t.Fatal("the request went on with the call")
	}
	panic("unreachable")
}

// fileMade closes the channel it returns once path exists.
func fileMade(path string) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(path); err == nil {
				close(ch)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return ch
}

// Esc during a call in the proxy, a tool of its own or an external one,
// stops that call alone: its result is what it printed and "[interrupted
// by the user]", and the model takes the next turn, which ends the request.
// With hide_work too, the call is shown then.
func TestInterruptCall(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		hide       bool
	}{
		{"own tool", "wait", false},
		{"external tool", "slow", false},
		{"external tool, hide_work", "slow", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &fakeProvider{replies: []*llm.Response{
				{ToolCalls: []llm.ToolCall{toolCall("c1", tc.tool, `{}`)}},
				{Text: "done"},
			}}
			var a *Agent
			var j *fakeJournal
			var cwd string
			if tc.hide {
				a, j, _, _, cwd = hidingAgent(t, prov)
			} else {
				a, j, _, _, cwd = newAgent(t, prov)
			}
			var running <-chan struct{}
			switch tc.tool {
			case "wait":
				w := &waitTool{running: make(chan struct{})}
				a.Tools.Add(w)
				running = w.running
			case "slow":
				script := "#!/bin/sh\n# aish:desc Slow\necho early\n: >started\nsleep 30\necho late\n"
				if err := os.WriteFile(filepath.Join(cwd, "slow"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				a.Tools = tools.Load(cwd)
				running = fileMade(filepath.Join(cwd, "started"))
			}
			start := time.Now()
			stopped, err := startInterrupted(t, a, cwd, running)
			if err != nil {
				t.Fatal(err)
			}
			if !stopped {
				t.Error("Interrupt found no call in progress")
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("took %v", d)
			}
			if got := kinds(j.es); got != "user assistant tool_result assistant" {
				t.Fatalf("journal %s", got)
			}
			r := j.es[2]
			want := "partial\n[interrupted by the user]"
			if tc.tool == "slow" {
				want = "early\n[interrupted by the user]"
			}
			if r.ToolCallID != "c1" || !r.IsError || r.Output != want {
				t.Errorf("result %+v, want %q", r, want)
			}
			if len(prov.requests) != 2 {
				t.Errorf("%d turns, want the one after the call too", len(prov.requests))
			}
			if a.Interrupt(ByUser) {
				t.Error("Interrupt found a call after the request")
			}
		})
	}
}

// Esc in a turn of the model finds no call: stopping the request is the
// host's. A command for the shell is not the agent's either once handed.
func TestInterruptTurn(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"sleep 30"}`)}},
	}}
	a, _, sh, _, cwd := newAgent(t, prov)
	var inTurn bool
	prov.before = func(context.Context, int, func(string)) { inTurn = a.Interrupt(ByUser) }
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if inTurn {
		t.Error("Interrupt stopped something in a turn")
	}
	if len(sh.handed) != 1 || a.Interrupt(ByUser) {
		t.Errorf("handed %q; Interrupt after the hand-off found a call", sh.handed)
	}
}

// A command the shell stopped for Esc comes back with its output and Why:
// the model gets both, the request goes on.
func TestResumeInterrupted(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"sleep 30"}`)}},
		{Text: "stopped it"},
	}}
	a, j, sh, _, cwd := newAgent(t, prov)
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	sh.outputs["c1"] = rpc.Output{Output: "waiting\n", Exit: 130, Cwd: cwd, Why: "interrupted by the user"}
	if err := a.Resume(context.Background(), "c1", 130, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	want := "waiting\n[exit 130, cwd " + cwd + "]\n[interrupted by the user]"
	if r := j.es[2]; r.Output != want {
		t.Errorf("result %q, want %q", r.Output, want)
	}
	if !strings.Contains(j.es[3].Text, "stopped it") {
		t.Errorf("no turn after the command: %+v", j.es[3])
	}
}
