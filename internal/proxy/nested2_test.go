package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// What the test binary, run as a subagent's command by
// TestNestedFromSubagent, asks the proxy at $AISH_SOCK, and the file it
// tells how each call ended in.
const (
	nestedCallsEnv = "PROXY_TEST_NESTED_CALLS"
	nestedOutEnv   = "PROXY_TEST_NESTED_OUT"
)

// TestNestedHelper is not a test of its own: it is the command of the
// subagent in TestNestedFromSubagent.
func TestNestedHelper(t *testing.T) {
	calls := os.Getenv(nestedCallsEnv)
	if calls == "" {
		t.Skip("the command of a subagent in TestNestedFromSubagent")
	}
	var b strings.Builder
	c, err := rpc.FromEnv()
	for _, m := range strings.Fields(calls) {
		if err == nil {
			ap := rpc.AgentParams{Text: "nested", ID: "t1", Cwd: "/"}
			fmt.Fprintf(&b, "%s: %v\n", m, c.CallContext(context.Background(), m, ap, nil))
		} else {
			fmt.Fprintf(&b, "%s: %v\n", m, err)
		}
	}
	_ = os.WriteFile(os.Getenv(nestedOutEnv), []byte(b.String()), 0o600)
	os.Exit(0)
}

// A subagent's command keeps $AISH_SOCK: `aish tool` calls MCP tools in
// the proxy. `aish compact`, `aish agent start` or `aish agent resume` from
// there would wait for the turn of the request that waits for the
// subagent; they are refused at once, and the request goes on.
func TestNestedFromSubagent(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task", Args: json.RawMessage(`{"tasks":[{"agent":"helper","prompt":"nest"}]}`)}}},
		bashCall("b1", "'"+exe+"' '-test.run=^TestNestedHelper$'"),
		{Text: "nested calls made"},
		{Text: "all done"},
	}}
	p, _, cwd := hosted(t, prov)
	dir := filepath.Join(cwd, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	def := "---\nname: helper\ndescription: Helps\n---\nHelp.\n"
	if err := os.WriteFile(filepath.Join(dir, "helper.md"), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(p.run, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go rpc.Serve(l, p.handle)

	out := filepath.Join(t.TempDir(), "nested")
	env := []string{
		"AISH_SOCK=" + sock,
		"PATH=" + os.Getenv("PATH"),
		nestedCallsEnv + "=" + strings.Join([]string{rpc.MethodCompact, rpc.MethodAgentStart, rpc.MethodAgentResume}, " "),
		nestedOutEnv + "=" + out,
	}
	p.marker(Marker{Kind: "ask-start"})
	done := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "delegate", Cwd: cwd, Env: env})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		p.cancelRequest()
		<-done
		t.Fatal("the request waits for its subagent's command, which waits for the request")
	}
	got, _ := os.ReadFile(out)
	for _, want := range []string{
		rpc.MethodCompact + ": " + errBusy.Error(),
		rpc.MethodAgentStart + ": " + errBusy.Error(),
		rpc.MethodAgentResume + ": the shell is not running the assistant's command t1",
	} {
		if !strings.Contains(string(got), want+"\n") {
			t.Errorf("no %q in what the subagent's command got:\n%s", want, got)
		}
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Errorf("journal %s", got)
	}
}

// gated is scripted with one turn held: the reply to call n waits for
// open, and entered tells it has begun.
type gated struct {
	scripted
	n       int
	entered chan struct{}
	open    chan struct{}
}

func (g *gated) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	g.mu.Lock()
	n := g.calls
	g.mu.Unlock()
	if n == g.n {
		close(g.entered)
		select {
		case <-g.open:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.scripted.Complete(ctx, req, onText)
}

// `aish compact &` in the agent's command: its call may come during the
// model's next turn, when the command's output is taken and the next one
// is not handed off yet. It is refused, and the next command keeps its
// call.
func TestCompactDuringTurn(t *testing.T) {
	g := &gated{scripted: scripted{replies: []*llm.Response{
		bashCall("c1", "aish compact &"),
		bashCall("c2", "ls"),
		{Text: "done"},
		{Text: "the summary"},
	}}, n: 1, entered: make(chan struct{}), open: make(chan struct{})}
	p, _, cwd := hosted(t, &g.scripted)
	p.newProvider = func(config.Config) (llm.Provider, error) { return g, nil }
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "tidy up", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c1;aish compact &"})
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;" + cwd})
	resumed := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", Cwd: cwd})
		resumed <- err
	}()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the next turn never began")
	}

	compacted := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodCompact, rpc.AgentParams{Cwd: cwd})
		compacted <- err
	}()
	var err error
	select {
	case err = <-compacted:
		close(g.open)
	case <-time.After(2 * time.Second):
		// Waiting for the turn: it gets one after the next hand-off.
		close(g.open)
		select {
		case err = <-compacted:
		case <-time.After(5 * time.Second):
			t.Fatal("compact still waits")
		}
	}
	if err == nil || err.Error() != errBusy.Error() {
		t.Errorf("compact during the turn: %v", err)
	}
	if err := <-resumed; err != nil {
		t.Fatal(err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c2;ls"})
	p.marker(Marker{Kind: "agent-end", Payload: "c2;0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c2", Cwd: cwd}); err != nil {
		t.Fatalf("the next command's agent_resume: %v", err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant tool_result assistant" {
		t.Errorf("journal %s", got)
	}
}

// A request that came with nothing in progress waits for the turn of one
// that came at the same time; when it gets the turn, that one may have
// left a command for the shell. It is refused then, and the command stays
// for the shell.
func TestStartAfterHandOff(t *testing.T) {
	p, _, cwd := hosted(t, &scripted{replies: []*llm.Response{{Text: "hi"}}})
	// The turn of the request that came first, before it marks itself in
	// progress.
	p.reqMu.Lock()
	started := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hello", Cwd: cwd})
		started <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		passed := p.asking
		p.mu.Unlock()
		if passed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent_start refused before its turn")
		}
	}
	// That request leaves its command and ends.
	if err := (shell{p}).HandOff("c1", "ls"); err != nil {
		t.Fatal(err)
	}
	p.reqMu.Unlock()
	select {
	case err := <-started:
		if err == nil || err.Error() != errNested.Error() {
			t.Errorf("agent_start after the hand-off: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent_start still waits")
	}
	if cmd, _ := os.ReadFile(filepath.Join(p.run, "next.cmd")); string(cmd) != "ls" {
		t.Errorf("next.cmd %q", cmd)
	}
	if got := journalKinds(p.sess); got != "" {
		t.Errorf("journal %s", got)
	}
}

// agent_resume goes on with the call the shell was handed, and with no
// other: from the agent's command, another pending call would be answered
// with no output, and the one running would be handed off again.
func TestResumeOtherCall(t *testing.T) {
	two := &llm.Response{ToolCalls: append(bashCall("c1", "ls").ToolCalls, bashCall("c2", "pwd").ToolCalls...)}
	p, _, cwd := hosted(t, &scripted{replies: []*llm.Response{two, {Text: "done"}}})
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "look", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	refused := func(id, when string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: id, Cwd: cwd})
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "the shell is not running the assistant's command "+id) {
				t.Errorf("agent_resume %s %s: %v", id, when, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("agent_resume %s %s waits for its output", id, when)
		}
	}
	refused("c2", "while c1 runs")
	p.marker(Marker{Kind: "agent-start", Payload: "c1;ls"})
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if id, _ := os.ReadFile(filepath.Join(p.run, "next.id")); string(id) != "c2\n" {
		t.Fatalf("handed off %q", id)
	}
	refused("c1", "again")
	p.marker(Marker{Kind: "agent-start", Payload: "c2;pwd"})
	p.marker(Marker{Kind: "agent-end", Payload: "c2;0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c2", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result tool_result assistant" {
		t.Errorf("journal %s", got)
	}
	refused("c2", "after the request")
}
