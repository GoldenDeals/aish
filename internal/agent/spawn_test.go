package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/tools"
)

// stateOf waits for subagent id in the background to be in state want.
func stateOf(t *testing.T, a *Agent, id, want string) rpc.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := a.BackgroundTask(id)
		if err == nil && got.State == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %+v, %v; want %s", id, got, err, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The user's subagent, &NAME text: in the background with no turn of the
// host's model and nothing in the journal; its answer is for aish tasks
// show, and Ended gives it once it is done, once. The model's tools do not
// see it: task_wait without ids does not wait for it, nor do task_result
// and task_cancel know it.
func TestSpawn(t *testing.T) {
	gate := make(chan struct{})
	prov := gatedSubs(hostTurns(), map[string]chan struct{}{"alpha": gate})
	a, j, _, _, cwd := newBgAgent(t, prov, def("alpha"), def("beta"))
	got, err := a.Spawn("alpha", "check the diff", tools.Exec{Dir: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "bg1" || got.Agent != "alpha" || got.Prompt != "check the diff" {
		t.Fatalf("spawned %+v", got)
	}
	if out := mustUse(t, a, subName, `{"tasks":[{"agent":"beta","prompt":"x"}],"background":true}`); out != "started bg2 (beta)" {
		t.Fatalf("task: %q", out)
	}
	if out := mustUse(t, a, taskWait, `{"timeout":5}`); out != "## bg2 beta (ok)\nbeta reply" {
		t.Errorf("task_wait without ids: %q", out)
	}
	if out := mustUse(t, a, taskResult, `{}`); out != "bg2 beta: ok" {
		t.Errorf("task_result lists %q", out)
	}
	for _, name := range []string{taskWait, taskResult, taskCancel} {
		if _, err := use(context.Background(), t, a, name, `{"ids":["bg1"]}`); err == nil || !strings.Contains(err.Error(), "user's own") {
			t.Errorf("%s of the user's: %v", name, err)
		}
	}
	if ended := a.Ended(); len(ended) != 0 {
		t.Errorf("ended while at work: %+v", ended)
	}
	close(gate)
	stateOf(t, a, "bg1", bgOK)
	ended := a.Ended()
	if len(ended) != 1 || ended[0].ID != "bg1" || ended[0].State != bgOK {
		t.Fatalf("ended %+v", ended)
	}
	if again := a.Ended(); len(again) != 0 {
		t.Errorf("told twice: %+v", again)
	}
	if out, _ := a.BackgroundTask("bg1"); !strings.Contains(out.Output, "alpha reply") {
		t.Errorf("aish tasks show bg1:\n%s", out.Output)
	}
	if got := kinds(j.es); got != "" {
		t.Errorf("journal %s", got)
	}
	for _, req := range prov.all() {
		if subOf(req) == "" {
			t.Fatal("the host's model had a turn")
		}
		if u := lastUser(req); subOf(req) == "alpha" && !strings.Contains(u.Text, "check the diff") {
			t.Errorf("alpha was told %q", u.Text)
		}
	}
}

// An unknown name, no text and a ninth subagent at work start nothing.
func TestSpawnRefused(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, _, _, _, cwd := newBgAgent(t, gatedSubs(hostTurns(), map[string]chan struct{}{"alpha": gate}), def("alpha"), def("beta"))
	ex := tools.Exec{Dir: cwd}
	if _, err := a.Spawn("nope", "x", ex); err == nil || err.Error() != "no subagent nope here (there are alpha, beta; aish agents tells of the files)" {
		t.Errorf("unknown: %v", err)
	}
	if _, err := a.Spawn("alpha", " \n", ex); err == nil {
		t.Error("no text started it")
	}
	for i := range maxUnfinished {
		if _, err := a.Spawn("alpha", "x", ex); err != nil {
			t.Fatalf("%d: %v", i+1, err)
		}
	}
	if _, err := a.Spawn("beta", "x", ex); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Errorf("a ninth: %v", err)
	}
	if out, err := a.BackgroundTask("bg9"); err == nil {
		t.Errorf("a ninth was kept: %+v", out)
	}
}
