package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute:         "10m",
		150 * time.Second:        "2m30s",
		37500 * time.Millisecond: "37.5s",
		18750 * time.Millisecond: "18.75s",
		15 * time.Second:         "15s",
		time.Hour:                "1h",
		90 * time.Minute:         "1h30m",
		24 * time.Hour:           "24h",
		50 * time.Millisecond:    "50ms",
	} {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
}

// waitingUI is a terminal whose questions of the policy get answers in
// turn, "" for none: then the question ends at its deadline at once, as the
// proxy ends it when the time is out. waits are what each question was
// given to wait.
type waitingUI struct {
	*fakeUI
	answers []string
	waits   []time.Duration
}

func (u *waitingUI) Ask(ctx context.Context, q string) (string, error) {
	u.asked = append(u.asked, q)
	dl, ok := ctx.Deadline()
	if !ok {
		return "", errors.New("the question has no deadline")
	}
	u.waits = append(u.waits, time.Until(dl))
	ans := u.answers[0]
	u.answers = u.answers[1:]
	if ans == "" {
		return "", context.DeadlineExceeded
	}
	return ans, nil
}

// askPolicy asks about every bash command.
func askPolicy(t *testing.T) *policy.Engine {
	t.Helper()
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n@ask(\"sure?\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

// asking is a model that runs n commands, one per turn, and then answers.
func asking(n int) *fakeProvider {
	prov := &fakeProvider{}
	for i := range n {
		id := fmt.Sprintf("c%d", i+1)
		prov.replies = append(prov.replies, &llm.Response{ToolCalls: []llm.ToolCall{toolCall(id, "bash", `{"command":"rm x"}`)}})
	}
	prov.replies = append(prov.replies, &llm.Response{Text: "fine"})
	return prov
}

// waited checks that each question was given the wait of want: the time
// left till its deadline, which was set to want a moment before.
func waited(t *testing.T, what string, got []time.Duration, want ...time.Duration) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(got); i++ {
		ok = got[i] <= want[i] && want[i]-got[i] < time.Second
	}
	if !ok {
		t.Errorf("%s: waited %v, want %v", what, got, want)
	}
}

const (
	sec  = time.Second
	mins = time.Minute
)

// A question of the policy left unanswered is a deny, and the request goes
// on: the first waits 10 minutes, each next one unanswered in a row half
// as long as the one before, down to 15 seconds.
func TestAskWaitHalves(t *testing.T) {
	const n = 8
	a, j, sh, ui, cwd := newAgent(t, asking(n))
	wui := &waitingUI{fakeUI: ui, answers: make([]string, n)}
	a.Policy, a.UI = askPolicy(t), wui
	if err := a.Start(context.Background(), "remove x", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	waited(t, "unanswered", wui.waits, 10*mins, 5*mins, 150*sec, 75*sec, 37500*time.Millisecond, 18750*time.Millisecond, 15*sec, 15*sec)
	if len(sh.handed) != 0 {
		t.Errorf("handed off %q", sh.handed)
	}
	var results []string
	for _, e := range j.es {
		if e.Kind == session.KindToolResult {
			if !e.IsError {
				t.Errorf("not an error: %+v", e)
			}
			results = append(results, e.Output)
		}
	}
	want := []string{"10m", "5m", "2m30s", "1m15s", "37.5s", "18.75s", "15s", "15s"}
	for i, s := range want {
		want[i] = "denied by policy: the user did not answer in " + s
	}
	if !reflect.DeepEqual(results, want) {
		t.Errorf("results %q", results)
	}
	if out := ui.String(); !strings.Contains(out, "✗ denied by policy: the user did not answer in 10m") || !strings.Contains(out, "fine") {
		t.Errorf("terminal %q", out)
	}
}

// An answer, No or Yes, gives the next question 10 minutes again, and so
// does a new request.
func TestAskWaitReset(t *testing.T) {
	a, _, sh, ui, cwd := newAgent(t, asking(5))
	wui := &waitingUI{fakeUI: ui, answers: []string{"", "", "n", "", ""}}
	a.Policy, a.UI = askPolicy(t), wui
	if err := a.Start(context.Background(), "remove x", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	waited(t, "No", wui.waits, 10*mins, 5*mins, 150*sec, 10*mins, 5*mins)
	if a.asks.wait() != 150*sec {
		t.Fatalf("left %v for the next question", a.asks.wait())
	}

	// The request is over, unanswered twice: the next starts from 10 minutes.
	a.Provider = asking(2)
	wui.answers, wui.waits = []string{"", ""}, nil
	if err := a.Start(context.Background(), "remove x", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	waited(t, "a new request", wui.waits, 10*mins, 5*mins)

	// Yes hands the command to the shell; the request waits for it, and
	// its next question for 10 minutes.
	a.Provider = asking(2)
	wui.answers, wui.waits = []string{"", "y"}, nil
	if err := a.Start(context.Background(), "remove x", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	waited(t, "Yes", wui.waits, 10*mins, 5*mins)
	if len(sh.handed) != 1 || a.asks.wait() != 10*mins {
		t.Errorf("after Yes: handed off %q, %v for the next question", sh.handed, a.asks.wait())
	}
}

// blockingUI holds a question of the policy, and the form of ask_user,
// open until its ctx ends, running hold meanwhile.
type blockingUI struct {
	*fakeUI
	hold func(ctx context.Context)
}

func (u *blockingUI) Ask(ctx context.Context, q string) (string, error) {
	u.asked = append(u.asked, q)
	u.hold(ctx)
	return "", ctx.Err()
}

func (u *blockingUI) Form(ctx context.Context, qs []Question) ([]Answer, error) {
	u.forms = append(u.forms, qs)
	u.hold(ctx)
	return nil, ctx.Err()
}

// held is the time each question was held open by blockingUI, and the
// cause its ctx ended with. The time runs from since, a moment before the
// question was asked: the test sets it before the request, each question
// for the next one at its end. The clock of a question starts before the UI
// gets it, so a time from the start of hold falls short of the wait by the
// delay between the two and has the question seem to end early.
type held struct {
	since  time.Time
	took   []time.Duration
	causes []string
}

func (h *held) hold(ctx context.Context) {
	if h.since.IsZero() {
		panic("held: since is not set before the request")
	}
	<-ctx.Done()
	end := time.Now()
	h.took = append(h.took, end.Sub(h.since))
	h.since = end
	h.causes = append(h.causes, context.Cause(ctx).Error())
}

// With the waits shortened, a question nobody answers ends at its deadline
// by the clock, with why: the UI shows it on the question.
func TestAskWaitClock(t *testing.T) {
	first, least := askFirst, askLeast
	t.Cleanup(func() { askFirst, askLeast = first, least })
	askFirst, askLeast = 80*time.Millisecond, 30*time.Millisecond
	a, j, _, ui, cwd := newAgent(t, asking(3))
	var h held
	a.Policy, a.UI = askPolicy(t), &blockingUI{fakeUI: ui, hold: h.hold}
	h.since = time.Now()
	if err := a.Start(context.Background(), "remove x", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"no answer in 80ms", "no answer in 40ms", "no answer in 30ms"}; !reflect.DeepEqual(h.causes, want) {
		t.Errorf("causes %q, want %q", h.causes, want)
	}
	for i, ms := range []time.Duration{80, 40, 30} {
		if i < len(h.took) && h.took[i] < ms*time.Millisecond {
			t.Errorf("question %d ended after %v", i+1, h.took[i])
		}
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant tool_result assistant tool_result assistant" {
		t.Errorf("journal %s", got)
	}
}

// The form of ask_user nobody answers is closed after ask_timeout: the
// model is told so, not as an error, and decides itself what follows.
func TestAskUserTimeout(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "ask_user", askArgs)}},
		{Text: "ok"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.AskTimeout = "50ms"
	var h held
	a.UI = &blockingUI{fakeUI: ui, hold: h.hold}
	h.since = time.Now()
	if err := a.Start(context.Background(), "make it nice", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(h.took) != 1 || h.took[0] < 50*time.Millisecond || h.causes[0] != "no answer in 50ms" {
		t.Errorf("the form closed after %v: %q", h.took, h.causes)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	if r := j.es[2]; r.IsError || r.Output != noAnswer(50*time.Millisecond) || !strings.Contains(r.Output, "did not answer in 50ms") {
		t.Errorf("result %+v", r)
	}
	if !strings.Contains(ui.String(), "✗ no answer in 50ms") {
		t.Errorf("terminal %q", ui.String())
	}
	if len(prov.requests) != 2 {
		t.Errorf("the request did not go on: %d turns", len(prov.requests))
	}
}

// ask_timeout is 5 minutes by default; "0" waits for ever.
func TestAskUserTimeoutOff(t *testing.T) {
	for _, tc := range []struct {
		timeout string
		want    time.Duration
	}{
		{"", 5 * mins},
		{"0", 0},
	} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", "ask_user", askArgs)}},
			{Text: "ok"},
		}}
		a, j, _, ui, cwd := newAgent(t, prov)
		if tc.timeout != "" {
			a.Cfg.AskTimeout = tc.timeout
		}
		var got time.Duration
		ui.form = func(ctx context.Context, _ []Question) ([]Answer, error) {
			if dl, ok := ctx.Deadline(); ok {
				got = time.Until(dl)
			}
			return []Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Colors"}}}, nil
		}
		if err := a.Start(context.Background(), "make it nice", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		if got > tc.want || tc.want-got > time.Second {
			t.Errorf("ask_timeout %q: the form waited %v", tc.timeout, got)
		}
		if r := j.es[2]; r.IsError || r.Output != "Approach: Patch\nFeatures: Colors" {
			t.Errorf("ask_timeout %q: result %+v", tc.timeout, r)
		}
	}
}
