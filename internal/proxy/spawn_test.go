package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// spawnHost answers a subagent once gate is open, and counts the turns of
// the host's model, which a subagent the user starts has none of.
type spawnHost struct {
	scripted
	gate chan struct{}
	host atomic.Int32
}

func (h *spawnHost) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	if !strings.Contains(req.System, "# Subagent") {
		h.host.Add(1)
		return nil, errors.New("the host's model had a turn")
	}
	select {
	case <-h.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if onText != nil {
		onText("the answer")
	}
	return &llm.Response{Text: "the answer", InputTokens: 10, OutputTokens: 5}, nil
}

// spawnProxy is a proxy whose directory has the subagent reviewer.
func spawnProxy(t *testing.T) (*Proxy, *terminal, string, *spawnHost) {
	t.Helper()
	h := &spawnHost{gate: make(chan struct{})}
	p, out, cwd := hosted(t, &h.scripted)
	p.newProvider = func(config.Config) (llm.Provider, error) { return h, nil }
	dir := filepath.Join(cwd, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("---\nname: reviewer\ndescription: Reviews\n---\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.stopBackground)
	return p, out, cwd, h
}

func spawnParams(cwd, agent, text string) rpc.SpawnParams {
	return rpc.SpawnParams{AgentParams: rpc.AgentParams{Text: text, Cwd: cwd}, Agent: agent}
}

// &reviewer check x: agent_spawn starts bg1 without a turn of the host's
// model, and the journal gets only what the subagent's turns cost. The
// first prompt after it finished tells so, once, on a line of its own;
// aish tasks show bg1 has its answer.
func TestAgentSpawn(t *testing.T) {
	p, out, cwd, h := spawnProxy(t)
	res, err := call(t, p, rpc.MethodAgentSpawn, spawnParams(cwd, "reviewer", "check x"))
	if err != nil {
		t.Fatal(err)
	}
	if task, _ := res.(rpc.Task); task.ID != "bg1" || task.Agent != "reviewer" || task.Prompt != "check x" {
		t.Fatalf("spawned %+v", res)
	}
	const told = "\x1b[2m[aish: reviewer finished in the background (bg1), see aish tasks show bg1]\x1b[0m\r\n"
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	if strings.Contains(out.String(), "finished") {
		t.Fatalf("told while at work:\n%q", out.String())
	}

	close(h.gate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := call(t, p, rpc.MethodTasks, rpc.TasksParams{ID: "bg1"})
		if task, _ := res.(rpc.Task); err == nil && task.State == "ok" {
			if !strings.Contains(task.Output, "the answer") {
				t.Errorf("aish tasks show bg1:\n%s", task.Output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bg1: %+v, %v", res, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := len(out.String())
	p.output([]byte("partial")) // a prompt the output left off the first column
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	if got := out.String()[before:]; got != "partial\r\n"+told {
		t.Errorf("at the prompts:\n%q\nwant\n%q", got, "partial\r\n"+told)
	}
	if n := h.host.Load(); n != 0 {
		t.Errorf("%d turns of the host's model", n)
	}
	es := p.session().Entries()
	if len(es) == 0 {
		t.Fatal("no spend in the journal")
	}
	for _, e := range es {
		if e.Kind != session.KindUsage || e.About != "reviewer" {
			t.Errorf("journal entry %s %q", e.Kind, e.About)
		}
	}
}

// Who may start one is who may start a request: not a process in the
// background of the shell's terminal, not the agent's own command. Nor
// does a name the directory has no subagent of start anything.
func TestAgentSpawnRefused(t *testing.T) {
	p, _, cwd, _ := spawnProxy(t)
	fg := p.fg
	p.fg = func() (int, error) { return 1, nil }
	if _, err := call(t, p, rpc.MethodAgentSpawn, spawnParams(cwd, "reviewer", "x")); !errors.Is(err, errNotShell) {
		t.Errorf("from the background: %v", err)
	}
	p.fg = fg
	p.mu.Lock()
	p.handed = "c1"
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodAgentSpawn, spawnParams(cwd, "reviewer", "x")); !errors.Is(err, errNested) {
		t.Errorf("from the agent's command: %v", err)
	}
	p.mu.Lock()
	p.handed = ""
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodAgentSpawn, spawnParams(cwd, "nope", "x")); err == nil || !strings.Contains(err.Error(), "no subagent nope here") {
		t.Errorf("an unknown one: %v", err)
	}
	if res, _ := call(t, p, rpc.MethodTasks, nil); len(res.([]rpc.Task)) != 0 {
		t.Errorf("started %+v", res)
	}
}
