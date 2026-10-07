package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/tools"
)

// liveBuf is the live output of a call, read by the test while the call
// writes it.
type liveBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *liveBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *liveBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// waitOn calls task_wait with args, its live output on live, in a
// goroutine.
func waitOn(ctx context.Context, t *testing.T, a *Agent, args string, live io.Writer) <-chan [2]any {
	t.Helper()
	tool, ok := a.Tools.Get(taskWait)
	if !ok {
		t.Fatal("no task_wait")
	}
	m, err := tools.Decode(json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan [2]any, 1)
	go func() {
		out, err := tool.Execute(ctx, a.exec, m, live)
		done <- [2]any{out, err}
	}()
	return done
}

// shows waits for the live output, as text, to be want.
func shows(t *testing.T, live *liveBuf, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for capture.Clean([]byte(live.String())) != want {
		if time.Now().After(deadline) {
			t.Fatalf("live output %q, want %q shown", live.String(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// task_wait streams: its call has a live output, and with no terminal
// only its answers go there.
func TestTaskWaitIsLive(t *testing.T) {
	prov := gatedSubs(hostTurns(
		callOf("t1", subName, `{"tasks":[{"agent":"alpha","prompt":"job A"}],"background":true}`),
		callOf("w1", taskWait, `{"ids":["bg1"],"timeout":5}`),
		&llm.Response{Text: "got it"},
	), nil)
	a, j, _, ui, cwd := newBgAgent(t, prov, def("alpha"))
	if err := a.Start(context.Background(), "start and wait", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ui.lives, "|") != "⚙ task alpha|⚙ task_wait bg1" || len(ui.folds) != 0 {
		t.Errorf("live %q, folded %q", ui.lives, ui.folds)
	}
	answer := "## bg1 alpha (ok)\nalpha reply"
	if r := j.es[4]; r.Output != answer {
		t.Errorf("task_wait gave %q", r.Output)
	}
	if !strings.Contains(ui.String(), " task_wait bg1\n"+answer+"\ngot it\n") || strings.Contains(ui.String(), "waiting") {
		t.Errorf("terminal:\n%q", ui.String())
	}
}

// While it waits, task_wait shows on its live output whom for, a line it
// takes off when the wait is over: what the live output keeps is the
// answers alone. It does not show the line when there is nothing to wait
// for, and takes it off when Ctrl+C stops the wait.
func TestTaskWaitLine(t *testing.T) {
	gates := map[string]chan struct{}{"alpha": make(chan struct{}), "beta": make(chan struct{})}
	a, _, _, ui, _ := newBgAgent(t, gatedSubs(hostTurns(), gates), def("alpha"), def("beta"))
	ui.cols = 80
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"},{"agent":"beta","prompt":"y"}],"background":true}`)

	live := &liveBuf{}
	done := waitOn(context.Background(), t, a, `{"ids":["bg1","bg2"]}`, live)
	shows(t, live, "waiting for bg1, bg2…")
	close(gates["alpha"])
	shows(t, live, "waiting for bg2…") // redrawn in place, as they finish
	close(gates["beta"])
	got := inTime(t, done, "task_wait")
	answer := "## bg1 alpha (ok)\nalpha reply\n\n## bg2 beta (ok)\nbeta reply"
	if got[0] != answer || got[1] != nil {
		t.Fatalf("task_wait: %q, %v", got[0], got[1])
	}
	raw := live.String()
	if text := capture.Clean([]byte(raw)); text != answer {
		t.Errorf("the live output keeps %q, want the answers alone", text)
	}
	before, after, ok := strings.Cut(raw, "\r\x1b[K")
	if !ok || !strings.HasPrefix(before, "\r"+dim+"waiting for bg1, bg2…") || after != answer+"\n" {
		t.Errorf("the line is not taken off before the answers: %q", raw)
	}

	// Finished already: no line.
	live = &liveBuf{}
	if got := inTime(t, waitOn(context.Background(), t, a, `{"ids":["bg1"]}`, live), "task_wait"); got[1] != nil {
		t.Fatal(got[1])
	}
	if raw := live.String(); raw != "## bg1 alpha (ok)\nalpha reply\n" {
		t.Errorf("a wait for nothing shows %q", raw)
	}

	// Ctrl+C: the line goes, and no answers come.
	gates["alpha"] = make(chan struct{})
	defer close(gates["alpha"])
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"z"}],"background":true}`)
	ctx, cancel := context.WithCancel(context.Background())
	live = &liveBuf{}
	done = waitOn(ctx, t, a, `{}`, live)
	shows(t, live, "waiting for bg3…")
	cancel()
	if got := inTime(t, done, "task_wait after Ctrl+C"); !errors.Is(got[1].(error), context.Canceled) {
		t.Fatalf("task_wait after Ctrl+C: %v", got[1])
	}
	if raw := live.String(); capture.Clean([]byte(raw)) != "" || !strings.HasSuffix(raw, "\r\x1b[K") {
		t.Errorf("after Ctrl+C the live output is %q", raw)
	}
}

// The line counts the seconds, fits the terminal, and a shorter text
// covers the longer one before it, so that the text the live output
// becomes holds the last one alone.
func TestWaitLineDraw(t *testing.T) {
	live := &liveBuf{}
	l := &waitLine{w: live, cols: 20, start: time.Now().Add(-3 * time.Second)}
	l.draw([]string{"bg1", "bg2"})
	if got := capture.Clean([]byte(live.String())); got != "waiting for bg1, b…" {
		t.Errorf("cut to the terminal: %q", got)
	}
	l.cols = 80
	l.draw([]string{"bg1", "bg2"})
	n := len(live.String())
	l.draw([]string{"bg1", "bg2"}) // the same text: not drawn again
	if len(live.String()) != n {
		t.Errorf("drawn again: %q", live.String())
	}
	l.draw([]string{"bg2"})
	if got := capture.Clean([]byte(live.String())); got != "waiting for bg2… 3s" {
		t.Errorf("redrawn: %q", got)
	}
	l.clear()
	if got := capture.Clean([]byte(live.String())); got != "" {
		t.Errorf("taken off: %q", got)
	}
	var none *waitLine
	none.draw([]string{"bg1"})
	none.clear()
	if a := (&Agent{UI: &fakeUI{}}); a.waitLine(live) != nil {
		t.Error("a line without a terminal")
	}
}

// What a subagent in the background shows is kept for aish tasks, while
// it works and once it is done; an id not kept is an error, as for
// task_result.
func TestBackgroundTaskOutput(t *testing.T) {
	gate := make(chan struct{})
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if len(lastUser(req).ToolResults) == 0 {
			return callOf("b1", tools.Bash, `{"command":"echo from-alpha"}`), nil
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return reply(&llm.Response{Text: "alpha ran it"}, onText)
	}
	a, _, _, _, _ := newBgAgent(t, prov, def("alpha"))

	if list := (&Agent{}).BackgroundTasks(); list != nil {
		t.Errorf("an agent without subagents lists %v", list)
	}
	if _, err := (&Agent{}).BackgroundTask("bg1"); err == nil || !strings.HasPrefix(err.Error(), "unknown id bg1:") {
		t.Errorf("bg1 of an agent without subagents: %v", err)
	}

	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"job A\nin two lines"}],"background":true}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := a.BackgroundTask("bg1")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(task.Output, "from-alpha\n") {
			if task.State != bgRunning {
				t.Errorf("state %q at work", task.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the output of the subagent at work: %q", task.Output)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":5}`)
	task, err := a.BackgroundTask("bg1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "bg1" || task.Agent != "alpha" || task.State != bgOK || task.Prompt != "job A\nin two lines" ||
		!strings.Contains(task.Output, "❯ echo from-alpha\nfrom-alpha\n") || !strings.HasSuffix(task.Output, "alpha ran it") ||
		strings.Contains(task.Output, "\x1b") {
		t.Errorf("the finished subagent: %+v", task)
	}
	want := []rpc.Task{{ID: "bg1", Agent: "alpha", State: bgOK, Prompt: "job A\nin two lines"}}
	if list := a.BackgroundTasks(); len(list) != 1 || list[0] != want[0] {
		t.Errorf("listed %+v, want %+v", list, want)
	}
	if _, err := a.BackgroundTask("bg9"); err == nil || err.Error() != "unknown id bg9: subagents in the background do not outlive aish, clear or resume" {
		t.Errorf("an unknown id: %v", err)
	}
	a.StopBackground()
	if _, err := a.BackgroundTask("bg1"); err == nil || !strings.HasPrefix(err.Error(), "unknown id bg1:") {
		t.Errorf("bg1 after StopBackground: %v", err)
	}
	if list := a.BackgroundTasks(); len(list) != 0 {
		t.Errorf("listed after StopBackground: %+v", list)
	}
}
