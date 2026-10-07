package proxy

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// aish tasks lists the subagents in the background and gives one with its
// output, between requests and before the first; an unknown id is an
// error.
func TestTasks(t *testing.T) {
	h := &bgHost{started: make(chan struct{}), stopped: make(chan struct{})}
	p, _, cwd := hosted(t, &h.scripted)
	p.newProvider = func(config.Config) (llm.Provider, error) { return h, nil }
	dir := filepath.Join(cwd, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helper.md"), []byte("---\nname: helper\ndescription: Helps\n---\nHelp.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.stopBackground)

	if got, err := p.handle(t.Context(), rpc.MethodTasks, nil); err != nil || !reflect.DeepEqual(got, []rpc.Task{}) {
		t.Errorf("before a request: %#v, %v", got, err)
	}
	if _, err := call(t, p, rpc.MethodTasks, rpc.TasksParams{ID: "bg1"}); err == nil || !strings.HasPrefix(err.Error(), "unknown id bg1:") {
		t.Errorf("bg1 before a request: %v", err)
	}

	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "start it", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	<-h.started
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})

	want := rpc.Task{ID: "bg1", Agent: "helper", State: "running", Prompt: "work"}
	if got, err := call(t, p, rpc.MethodTasks, nil); err != nil || !reflect.DeepEqual(got, []rpc.Task{want}) {
		t.Errorf("listed %#v, %v", got, err)
	}
	if got, err := call(t, p, rpc.MethodTasks, rpc.TasksParams{ID: "bg1"}); err != nil || got != want {
		t.Errorf("bg1: %#v, %v", got, err)
	}
	if _, err := call(t, p, rpc.MethodTasks, rpc.TasksParams{ID: "bg2"}); err == nil || !strings.HasPrefix(err.Error(), "unknown id bg2:") {
		t.Errorf("an unknown id: %v", err)
	}
}
