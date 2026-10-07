package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/subagent"
	"github.com/inebotov/aish/internal/tools"
)

// hostTurns answers the host's turns with turns, in order.
func hostTurns(turns ...*llm.Response) func(llm.Request) *llm.Response {
	var mu sync.Mutex
	n := 0
	return func(llm.Request) *llm.Response {
		mu.Lock()
		defer mu.Unlock()
		if n == len(turns) {
			return &llm.Response{Text: "no turn scripted"}
		}
		n++
		return turns[n-1]
	}
}

func callOf(id, name, args string) *llm.Response {
	return &llm.Response{ToolCalls: []llm.ToolCall{toolCall(id, name, args)}}
}

// gatedSubs is a provider whose host follows host and whose subagents
// answer "NAME reply" once their gate is open; a subagent without a gate
// answers at once.
func gatedSubs(host func(llm.Request) *llm.Response, gates map[string]chan struct{}) *subProvider {
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		name := subOf(req)
		if name == "" {
			return reply(host(req), onText)
		}
		if g := gates[name]; g != nil {
			select {
			case <-g:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return reply(&llm.Response{Text: name + " reply"}, onText)
	}
	return prov
}

// newBgAgent is an agent with subagents defs whose background is stopped
// when the test ends.
func newBgAgent(t *testing.T, prov *subProvider, defs ...subagent.Def) (*Agent, *fakeJournal, *fakeShell, *fakeUI, string) {
	t.Helper()
	a, j, sh, ui, cwd := newSubAgent(t, prov, defs...)
	a.exec = tools.Exec{Dir: cwd}
	t.Cleanup(a.StopBackground)
	return a, j, sh, ui, cwd
}

// use calls the agent's tool name directly, as the model would.
func use(ctx context.Context, t *testing.T, a *Agent, name, args string) (string, error) {
	t.Helper()
	tool, ok := a.Tools.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	m, err := tools.Decode(json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(ctx, a.exec, m, nil)
}

func mustUse(t *testing.T, a *Agent, name, args string) string {
	t.Helper()
	out, err := use(context.Background(), t, a, name, args)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

// eventually waits for task_result without ids to list want.
func eventually(t *testing.T, a *Agent, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := mustUse(t, a, taskResult, `{}`)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task_result lists\n%s\nwant\n%s", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func inTime[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: not in 5 s", what)
	}
	var zero T
	return zero
}

const stillRunning = "[aish: 1 subagent still running in the background, see aish tasks]"

// task with background returns while its subagent works; the answer comes
// in a later request, with task_wait, which waits for it. The request that
// ends with the subagent at work says so, once.
func TestTaskInBackground(t *testing.T) {
	gate := make(chan struct{})
	prov := gatedSubs(hostTurns(
		callOf("t1", subName, `{"tasks":[{"agent":"alpha","prompt":"job A"}],"background":true}`),
		&llm.Response{Text: "started it"},
		callOf("w1", taskWait, `{"ids":["bg1"]}`),
		&llm.Response{Text: "got it"},
	), map[string]chan struct{}{"alpha": gate})
	a, j, _, ui, cwd := newBgAgent(t, prov, def("alpha"))
	ex := tools.Exec{Dir: cwd}

	done := make(chan error, 1)
	go func() { done <- a.Start(context.Background(), "start alpha", ex) }()
	if err := inTime(t, done, "the request with task in the background"); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2]; r.Output != "started bg1 (alpha)" || r.IsError {
		t.Fatalf("result %q, error %v", r.Output, r.IsError)
	}
	if len(ui.lives) != 1 || ui.lives[0] != "⚙ task alpha" || !strings.Contains(ui.String(), "started bg1 (alpha)\n") {
		t.Errorf("live folds %q, terminal:\n%s", ui.lives, ui.String())
	}
	if n := strings.Count(ui.String(), stillRunning); n != 1 {
		t.Errorf("%d notes of the subagent at work:\n%s", n, ui.String())
	}

	go func() { done <- a.Start(context.Background(), "take it", ex) }()
	select {
	case err := <-done:
		t.Fatalf("task_wait did not wait for the subagent: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(gate)
	if err := inTime(t, done, "the request with task_wait"); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	if r := j.es[6]; r.Output != "## bg1 alpha (ok)\nalpha reply" || r.IsError {
		t.Errorf("task_wait gave %q, error %v", r.Output, r.IsError)
	}
	if n := strings.Count(ui.String(), stillRunning); n != 1 {
		t.Errorf("%d notes after the subagent ended:\n%s", n, ui.String())
	}
	for _, req := range prov.all() {
		if subOf(req) == "" {
			continue
		}
		for _, name := range []string{subName, taskWait, taskResult, taskCancel} {
			if slices.Contains(toolNames(req), name) {
				t.Errorf("the subagent got %s", name)
			}
		}
	}
}

// task_wait gives the subagents at work as such once its time is out,
// without an error; Ctrl+C stops the wait and not the subagent; without
// ids it waits for any one and gives the answers not taken yet.
func TestTaskWait(t *testing.T) {
	gate := make(chan struct{})
	a, _, _, _, _ := newBgAgent(t, gatedSubs(hostTurns(), map[string]chan struct{}{"alpha": gate}), def("alpha"), def("beta"))
	if got := mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"}],"background":true}`); got != "started bg1 (alpha)" {
		t.Fatalf("task: %q", got)
	}
	start := time.Now()
	if got := mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":0.1}`); got != "## bg1 alpha (running)" {
		t.Errorf("task_wait past its time: %q", got)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Errorf("task_wait of 0.1 s took %s", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() {
		_, err := use(ctx, t, a, taskWait, `{"ids":["bg1"]}`)
		waited <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := inTime(t, waited, "task_wait after Ctrl+C"); !errors.Is(err, context.Canceled) {
		t.Errorf("task_wait after Ctrl+C: %v", err)
	}
	eventually(t, a, "bg1 alpha: running")

	// beta, done at once, is the one an unnamed wait gets; alpha, at work,
	// is a heading.
	mustUse(t, a, subName, `{"tasks":[{"agent":"beta","prompt":"y"}],"background":true}`)
	if got := mustUse(t, a, taskWait, `{"timeout":5}`); got != "## bg1 alpha (running)\n\n## bg2 beta (ok)\nbeta reply" {
		t.Errorf("task_wait without ids: %q", got)
	}
	go func() {
		out, err := use(context.Background(), t, a, taskWait, `{}`)
		if err == nil && out != "## bg1 alpha (ok)\nalpha reply" {
			err = fmt.Errorf("gave %q", out)
		}
		waited <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(gate)
	if err := inTime(t, waited, "task_wait for any"); err != nil {
		t.Error(err)
	}
	if got := mustUse(t, a, taskWait, `{}`); !strings.HasPrefix(got, "No subagent is at work") {
		t.Errorf("task_wait with every answer taken: %q", got)
	}
	if got := mustUse(t, a, taskWait, `{"ids":["bg1","bg2"]}`); got != "## bg1 alpha (ok)\nalpha reply\n\n## bg2 beta (ok)\nbeta reply" {
		t.Errorf("answers taken again: %q", got)
	}
}

// task_result does not wait: it lists the states, or gives the answers
// there are, as often as it is asked. An id it does not know is the
// model's mistake.
func TestTaskResult(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, _, _, _, _ := newBgAgent(t, gatedSubs(hostTurns(), map[string]chan struct{}{"alpha": gate}), def("alpha"), def("beta"))
	if got := mustUse(t, a, taskResult, `{}`); got != "No subagents in the background." {
		t.Errorf("task_result of none: %q", got)
	}
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"},{"agent":"beta","prompt":"y"}],"background":"true"}`)
	eventually(t, a, "bg1 alpha: running\nbg2 beta: ok")
	for range 2 {
		if got := mustUse(t, a, taskResult, `{"ids":["bg1","bg2"]}`); got != "## bg1 alpha (running)\n\n## bg2 beta (ok)\nbeta reply" {
			t.Errorf("task_result: %q", got)
		}
	}
	_, err := use(context.Background(), t, a, taskResult, `{"ids":["bg2","bg9"]}`)
	if err == nil || err.Error() != "unknown id bg9: subagents in the background do not outlive aish, clear or resume" {
		t.Errorf("an unknown id: %v", err)
	}
}

// The subagent outlives the request: Ctrl+C on the turn after task, and
// the end of the request's context, leave it at work.
func TestBackgroundOutlivesRequest(t *testing.T) {
	gate := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	prov := gatedSubs(nil, map[string]chan struct{}{"alpha": gate})
	sub := prov.answer
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		switch {
		case subOf(req) != "":
			return sub(ctx, req, onText)
		case len(lastUser(req).ToolResults) > 0:
			cancel() // Ctrl+C while the model takes the result in
			return nil, ctx.Err()
		}
		return callOf("t1", subName, `{"tasks":[{"agent":"alpha","prompt":"x"}],"background":true}`), nil
	}
	a, j, _, _, cwd := newBgAgent(t, prov, def("alpha"))
	if err := a.Start(ctx, "go", tools.Exec{Dir: cwd}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if r := j.es[2]; r.Output != "started bg1 (alpha)" {
		t.Fatalf("result %q", r.Output)
	}
	eventually(t, a, "bg1 alpha: running")
	close(gate)
	if got := mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":5}`); got != "## bg1 alpha (ok)\nalpha reply" {
		t.Errorf("task_wait: %q", got)
	}
}

// failing is the provider of a request after the call of task.
type failing struct{ subProvider }

func (*failing) Complete(context.Context, llm.Request, func(string)) (*llm.Response, error) {
	return nil, errors.New("the next request's provider")
}

// A subagent in the background has what it needs of the host from the
// call: the next request's prepare sets the host's fields anew while it
// works (and -race tells if it reads them).
func TestBackgroundTakesTheHostAsCalled(t *testing.T) {
	gate := make(chan struct{})
	var result string
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if rs := lastUser(req).ToolResults; len(rs) > 0 {
			result = rs[0].Content
			return reply(&llm.Response{Text: "alpha ran it"}, onText)
		}
		return callOf("b1", tools.Bash, `{"command":"echo hi"}`), nil
	}
	a, _, _, _, cwd := newBgAgent(t, prov, def("alpha"))
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"}],"background":true}`)
	cfg := a.Cfg
	cfg.Shell, cfg.MaxOutputBytes = "/nonexistent/shell", 1
	a.Cfg, a.Provider, a.Policy, a.exec = cfg, &failing{}, &policy.Engine{}, tools.Exec{Dir: "/"}
	a.Tools = tools.Load("")
	a.AddSubagents([]subagent.Def{def("beta")})
	close(gate)
	if got := mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":10}`); got != "## bg1 alpha (ok)\nalpha ran it" {
		t.Errorf("task_wait: %q", got)
	}
	if want := "hi\n[exit 0, cwd " + cwd + "]"; result != want {
		t.Errorf("the command gave %q, want %q", result, want)
	}
}

// fifoCommand is a subagent's command that holds the FIFO at path open in
// a process it leaves in the background; the FIFO reads to its end once
// every process of the command's group is gone. opened tells the command
// has begun, ended that its processes are gone.
func fifoCommand(t *testing.T, path string) (cmd string, opened, ended chan struct{}) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	opened, ended = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		f, err := os.Open(path) // until the command opens it to write
		close(opened)
		if err != nil {
			return
		}
		defer f.Close()
		io.Copy(io.Discard, f)
	}()
	b, _ := json.Marshal(map[string]string{"command": "sleep 30 > '" + path + "' & wait"})
	return string(b), opened, ended
}

// task_cancel stops a subagent with the process group of its command, and
// so does StopBackground, which forgets them; both return once the
// processes are gone.
func TestTaskCancelKillsCommands(t *testing.T) {
	dir := t.TempDir()
	cmds := map[string]string{}
	var opened, ended [2]chan struct{}
	cmds["alpha"], opened[0], ended[0] = fifoCommand(t, filepath.Join(dir, "a"))
	cmds["beta"], opened[1], ended[1] = fifoCommand(t, filepath.Join(dir, "b"))
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sub := subOf(req)
		if len(lastUser(req).ToolResults) > 0 {
			return reply(&llm.Response{Text: sub + " ran it"}, onText)
		}
		return callOf("b1", tools.Bash, cmds[sub]), nil
	}
	a, _, _, _, _ := newBgAgent(t, prov, def("alpha"), def("beta"))

	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"}],"background":true}`)
	inTime(t, opened[0], "alpha's command")
	if got := mustUse(t, a, taskCancel, `{"ids":["bg1"]}`); got != "cancelled bg1 (alpha)" {
		t.Errorf("task_cancel: %q", got)
	}
	select {
	case <-ended[0]:
	case <-time.After(500 * time.Millisecond):
		t.Error("alpha's command outlived task_cancel")
	}
	if got := mustUse(t, a, taskResult, `{"ids":["bg1"]}`); got != "## bg1 alpha (cancelled)\n(no reply)" {
		t.Errorf("task_result of the cancelled one: %q", got)
	}

	mustUse(t, a, subName, `{"tasks":[{"agent":"beta","prompt":"y"}],"background":true}`)
	inTime(t, opened[1], "beta's command")
	a.StopBackground()
	select {
	case <-ended[1]:
	case <-time.After(500 * time.Millisecond):
		t.Error("beta's command outlived StopBackground")
	}
	if _, err := use(context.Background(), t, a, taskResult, `{"ids":["bg2"]}`); err == nil || !strings.HasPrefix(err.Error(), "unknown id bg2:") {
		t.Errorf("task_result after StopBackground: %v", err)
	}
	if got := mustUse(t, a, taskResult, `{}`); got != "No subagents in the background." {
		t.Errorf("listed after StopBackground: %q", got)
	}
}

// maxParallel run at once and the rest queue; maxUnfinished may be
// unfinished, and a call past them fails; maxFinished are kept.
func TestBackgroundLimits(t *testing.T) {
	gate := make(chan struct{})
	a, _, _, _, _ := newBgAgent(t, gatedSubs(hostTurns(), map[string]chan struct{}{"alpha": gate}), def("alpha"), def("beta"))
	tasks := func(name string, n int) string {
		var ts []string
		for range n {
			ts = append(ts, `{"agent":"`+name+`","prompt":"x"}`)
		}
		return `{"tasks":[` + strings.Join(ts, ",") + `],"background":true}`
	}
	mustUse(t, a, subName, tasks("alpha", 5))
	eventually(t, a, "bg1 alpha: running\nbg2 alpha: running\nbg3 alpha: running\nbg4 alpha: running\nbg5 alpha: queued")
	mustUse(t, a, subName, tasks("alpha", 3))
	_, err := use(context.Background(), t, a, subName, tasks("beta", 1))
	if err == nil || !strings.Contains(err.Error(), "would make 9 unfinished, and at most 8 may be") {
		t.Fatalf("a ninth unfinished: %v", err)
	}
	// A queued one is cancelled at once, and leaves room for another.
	if got := mustUse(t, a, taskCancel, `{"ids":["bg8"]}`); got != "cancelled bg8 (alpha)" {
		t.Errorf("task_cancel of a queued one: %q", got)
	}
	mustUse(t, a, subName, tasks("beta", 1))
	close(gate)
	mustUse(t, a, taskWait, `{"ids":["bg1","bg2","bg3","bg4","bg5","bg6","bg7","bg8","bg9"],"timeout":10}`)
	id := 9
	for range 4 {
		mustUse(t, a, subName, tasks("beta", 8))
		var ids []string
		for range 8 {
			id++
			ids = append(ids, fmt.Sprintf(`"bg%d"`, id))
		}
		mustUse(t, a, taskWait, `{"ids":[`+strings.Join(ids, ",")+`],"timeout":10}`)
	}
	list := strings.Split(mustUse(t, a, taskResult, `{}`), "\n")
	if len(list) != maxFinished || list[0] != "bg10 beta: ok" || list[len(list)-1] != "bg41 beta: ok" {
		t.Errorf("%d kept: %q", len(list), list)
	}
	_, err = use(context.Background(), t, a, taskResult, `{"ids":["bg1"]}`)
	if err == nil || err.Error() != "bg1 is no longer kept: only the last 32 finished subagents are" {
		t.Errorf("the oldest: %v", err)
	}
}

// A subagent gets none of the tools that run subagents, by any name.
func TestSubagentHasNoTaskTools(t *testing.T) {
	a := &Agent{Tools: tools.Load("")}
	a.AddSubagents([]subagent.Def{def("alpha")})
	for _, name := range []string{subName, taskWait, taskResult, taskCancel} {
		if _, ok := a.Tools.Get(name); !ok {
			t.Errorf("the host has no %s", name)
		}
	}
	for _, names := range [][]string{nil, {"task", "Task_Wait", "task_result", "TASK_CANCEL", "Read"}} {
		reg, _ := subTools(a.Tools, names)
		for _, tool := range reg.All() {
			if hostOnly(tool) || strings.HasPrefix(tool.Name(), "task") {
				t.Errorf("%q gave the subagent %s", names, tool.Name())
			}
		}
	}
}

// The tools of the background stay while it has subagents, in a directory
// without any too, and go with them.
func TestBackgroundToolsWithoutSubagents(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, _, _, _, _ := newBgAgent(t, gatedSubs(hostTurns(), map[string]chan struct{}{"alpha": gate}), def("alpha"))
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"}],"background":true}`)
	a.Tools = tools.Load("")
	a.AddSubagents(nil)
	if _, ok := a.Tools.Get(subName); ok {
		t.Error("task without subagents")
	}
	if _, ok := a.Tools.Get(taskWait); !ok {
		t.Error("no task_wait while a subagent is in the background")
	}
	a.StopBackground()
	a.Tools = tools.Load("")
	a.AddSubagents(nil)
	if _, ok := a.Tools.Get(taskWait); ok {
		t.Error("task_wait with nothing in the background")
	}
}

// The note comes as the request ends, after the command it handed the
// shell, and not at all without subagents at work.
func TestBackgroundNoteOncePerRequest(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	prov := gatedSubs(hostTurns(
		&llm.Response{Text: "nothing to start"},
		callOf("t1", subName, `{"tasks":[{"agent":"alpha","prompt":"x"}],"background":true}`),
		callOf("c1", tools.Bash, `{"command":"ls"}`),
		&llm.Response{Text: "listed"},
	), map[string]chan struct{}{"alpha": gate})
	a, _, sh, ui, cwd := newBgAgent(t, prov, def("alpha"))
	ex := tools.Exec{Dir: cwd}
	if err := a.Start(context.Background(), "hi", ex); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ui.String(), "in the background") {
		t.Errorf("a note without subagents:\n%s", ui.String())
	}
	if err := a.Start(context.Background(), "start and list", ex); err != nil {
		t.Fatal(err)
	}
	if len(sh.handed) != 1 || strings.Contains(ui.String(), stillRunning) {
		t.Fatalf("handed %q; terminal:\n%s", sh.handed, ui.String())
	}
	sh.outputs["c1"] = rpc.Output{Output: "a\n", Cwd: cwd}
	if err := a.Resume(context.Background(), "c1", 0, ex); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(ui.String(), stillRunning); n != 1 || !strings.HasSuffix(ui.String(), dim+stillRunning+reset+"\n") {
		t.Errorf("%d notes:\n%s", n, ui.String())
	}
}
