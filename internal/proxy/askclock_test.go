package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// lookOften has the clock standing look for its question every 5ms, so
// that it goes on as soon as the viewer is closed.
func lookOften(t *testing.T) {
	t.Helper()
	look := askLook
	t.Cleanup(func() { askLook = look })
	askLook = 5 * time.Millisecond
}

// locked is f under p.mu.
func locked[T any](p *Proxy, f func() T) T {
	p.mu.Lock()
	defer p.mu.Unlock()
	return f()
}

// viewOver opens the viewer over the open question with Ctrl+O.
func viewOver(t *testing.T, p *Proxy) {
	t.Helper()
	locked(p, func() bool { p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}; return true })
	p.key([]byte{ctrlO})
	if !locked(p, func() bool { return p.view != nil }) {
		t.Fatal("no viewer")
	}
}

// closeView closes the viewer with q and tells when.
func closeViewer(t *testing.T, p *Proxy) time.Time {
	t.Helper()
	p.key([]byte("q"))
	if locked(p, func() bool { return p.view != nil }) {
		t.Fatal("the viewer stayed open")
	}
	return time.Now()
}

// closedIn checks that the question ended took after the viewer closed:
// with the time it had left, about 100ms, not at once and not much later.
func closedIn(t *testing.T, what string, took time.Duration) {
	t.Helper()
	if took < 80*time.Millisecond || took > 400*time.Millisecond {
		t.Errorf("%s: ended %v after the viewer closed, want about 100ms", what, took)
	}
}

type askResult struct {
	ans string
	err error
}

// The time of a question stands while the viewer opened over it is open:
// 200ms to answer, Ctrl+O after 100ms, half a second in the viewer, and
// the question is still open; it ends with No some 100ms after the viewer
// closes, as at its time out.
func TestAskClockStandsInViewer(t *testing.T) {
	lookOften(t)
	p, out := termProxy(t)
	ctx := agent.WithAnswerTime(context.Background(), 200*time.Millisecond)
	res := make(chan askResult, 1)
	go func() {
		ans, err := p.askUser(ctx, "allow?")
		res <- askResult{ans, err}
	}()
	waitOpen(t, p, func() bool { return p.ask != nil })
	time.Sleep(100 * time.Millisecond)
	viewOver(t, p)
	time.Sleep(500 * time.Millisecond)
	if !locked(p, func() bool { return p.ask != nil }) {
		t.Fatalf("the question ended under the viewer: %q", out.String())
	}
	closed := closeViewer(t, p)
	select {
	case r := <-res:
		closedIn(t, "the question", time.Since(closed))
		if r.ans != "" || !errors.Is(r.err, context.DeadlineExceeded) {
			t.Errorf("answered %q, %v", r.ans, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question never ended")
	}
	if s := out.String(); !strings.HasSuffix(modeless(s), "\x1b[14DNo (no answer in 200ms)\x1b[K\r\n\x1b[?25h") {
		t.Errorf("terminal %q", s)
	}
	if got := p.key([]byte("y")); string(got) != "y" {
		t.Errorf("the keys after it: %q", got)
	}
}

// A question that opens under the viewer waits for it to close before its
// time goes; Ctrl+C, the request's end, closes it there at once.
func TestAskClockOpensUnderViewer(t *testing.T) {
	lookOften(t)
	p, out := termProxy(t)
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	ctx := agent.WithAnswerTime(context.Background(), 100*time.Millisecond)
	res := make(chan askResult, 1)
	go func() {
		ans, err := p.askUser(ctx, "allow?")
		res <- askResult{ans, err}
	}()
	waitOpen(t, p, func() bool { return p.ask != nil })
	time.Sleep(300 * time.Millisecond)
	if !locked(p, func() bool { return p.ask != nil }) {
		t.Fatal("the question ended under the viewer")
	}
	closed := closeViewer(t, p)
	r := <-res
	closedIn(t, "opened under the viewer", time.Since(closed))
	if !errors.Is(r.err, context.DeadlineExceeded) || !strings.Contains(out.String(), "No (no answer in 100ms)") {
		t.Errorf("%v, terminal %q", r.err, out.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = agent.WithAnswerTime(ctx, 10*time.Second)
	go func() {
		ans, err := p.askUser(ctx, "allow?")
		res <- askResult{ans, err}
	}()
	waitOpen(t, p, func() bool { return p.ask != nil })
	viewOver(t, p)
	cancel()
	select {
	case r := <-res:
		if !errors.Is(r.err, context.Canceled) {
			t.Errorf("the request ended: %v", r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("the request ended, the question stayed under the viewer")
	}
	if locked(p, func() bool { return p.ask != nil || p.view == nil }) {
		t.Error("the question stayed open, or the viewer went with it")
	}
	closeViewer(t, p)
}

// The form of ask_user as a request asks it, ask_timeout 200ms: Ctrl+O
// after 100ms, half a second in the viewer, the form still open, and the
// model told nobody answered some 100ms after the viewer closes.
func TestFormClockStandsInViewer(t *testing.T) {
	lookOften(t)
	args := `{"questions":[{"question":"Which approach?","header":"Approach","options":[{"label":"Rewrite"},{"label":"Patch"}]}]}`
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "ask_user", Args: json.RawMessage(args)}}},
		{Text: "going on without it"},
	}}
	p, out, cwd := hosted(t, prov)
	if err := os.WriteFile(os.Getenv("AISH_CONFIG"), []byte("ask_timeout = \"200ms\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.size = func() (int, int) { return 80, 24 }
	p.marker(Marker{Kind: "ask-start"})
	done := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "make it nice", Cwd: cwd})
		done <- err
	}()
	waitOpen(t, p, func() bool { return p.form != nil })
	time.Sleep(100 * time.Millisecond)
	viewOver(t, p)
	time.Sleep(500 * time.Millisecond)
	if !locked(p, func() bool { return p.form != nil }) {
		t.Fatal("the form went under the viewer")
	}
	closed := closeViewer(t, p)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request never ended")
	}
	took := time.Since(closed)
	if r := p.sess.Entries()[2]; r.IsError || !strings.Contains(r.Output, "did not answer in 200ms") {
		t.Errorf("result %+v", r)
	}
	// The request ends a turn of the model later.
	closedIn(t, "the form", took)
	if s := out.String(); !strings.Contains(s, "✗ no answer in 200ms") || !strings.Contains(s, "going on without it") {
		t.Errorf("terminal %q", s)
	}
}

// pauseFor has the clock of a question stand d in all.
func pauseFor(t *testing.T, d time.Duration) {
	t.Helper()
	pause := askPause
	t.Cleanup(func() { askPause = pause })
	askPause = d
}

// A viewer left open over a question does not keep it for ever: the time,
// 100ms, stands 150ms in all and then goes on under the viewer, and the
// question ends there with No some 250ms after it opened. The form that
// opens next under the same viewer has its own 150ms to stand.
func TestAskClockPauseRunsOut(t *testing.T) {
	lookOften(t)
	pauseFor(t, 150*time.Millisecond)
	p, out := termProxy(t)
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	ctx := agent.WithAnswerTime(context.Background(), 100*time.Millisecond)
	res := make(chan askResult, 1)
	start := time.Now()
	go func() {
		ans, err := p.askUser(ctx, "allow?")
		res <- askResult{ans, err}
	}()
	select {
	case r := <-res:
		if took := time.Since(start); took < 250*time.Millisecond || took > 3*time.Second {
			t.Errorf("the question ended %v after it opened, want 250ms", took)
		}
		if r.ans != "" || !errors.Is(r.err, context.DeadlineExceeded) {
			t.Errorf("answered %q, %v", r.ans, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question stood under the viewer for ever")
	}
	if locked(p, func() bool { return p.ask != nil || p.view == nil }) {
		t.Fatal("the question stayed open, or the viewer went with it")
	}

	form := make(chan formResult, 1)
	start = time.Now()
	go func() {
		ans, err := p.askForm(ctx, twoQuestions())
		form <- formResult{ans, err}
	}()
	r := result(t, form)
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Errorf("the form ended %v after it opened, want 250ms", took)
	}
	if r.ans != nil || !errors.Is(r.err, context.DeadlineExceeded) {
		t.Errorf("the form: %+v", r)
	}
	if locked(p, func() bool { return p.form != nil || p.view == nil }) {
		t.Fatal("the form stayed open, or the viewer went with it")
	}

	closeViewer(t, p)
	if s := out.String(); !strings.Contains(s, "No (no answer in 100ms)") {
		t.Errorf("terminal %q", s)
	}
	if got := p.key([]byte("y")); string(got) != "y" {
		t.Errorf("the keys after them: %q", got)
	}
}

// The clock stands askPause in all, not each time the viewer covers the
// question. 300ms to answer, 400ms to stand: 300ms under the viewer, 50ms
// in sight, then under the viewer again for good; the time is out 700ms
// after the start, the pause and the time to answer both gone, and not
// 1s, as a new pause for the viewer opened again would have it.
func TestAskClockPauseInAll(t *testing.T) {
	lookOften(t)
	var mu sync.Mutex
	hid := true
	c := &askClock{mu: &mu, hidden: func() bool { return hid }, left: 300 * time.Millisecond, pause: 400 * time.Millisecond, out: make(chan struct{})}
	start := time.Now()
	mu.Lock()
	c.sync()
	mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	hid = false // closed with nobody telling: the clock finds out by itself
	mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	hid = true
	c.sync()
	mu.Unlock()
	select {
	case <-c.ranOut():
		if took := time.Since(start); took < 650*time.Millisecond || took > 900*time.Millisecond {
			t.Errorf("out %v after the start, want 700ms", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the time never ran out")
	}
}
