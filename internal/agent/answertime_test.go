package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// timingUI keeps the time of its questions itself, as the proxy does, and
// nobody answers them: each ends as its time is out.
type timingUI struct {
	*hiderUI
	times     []time.Duration
	whys      []string
	deadlines int    // questions whose ctx had a deadline all the same
	while     func() // runs while a question is open
}

func (*timingUI) TimesAnswers() {}

func (u *timingUI) out(ctx context.Context) error {
	if u.while != nil {
		u.while()
	}
	if _, ok := ctx.Deadline(); ok {
		u.deadlines++
	}
	d, why, ok := AnswerTime(ctx)
	if !ok {
		return errors.New("no time on ctx")
	}
	u.times = append(u.times, d)
	u.whys = append(u.whys, why.Error())
	return context.DeadlineExceeded
}

func (u *timingUI) Ask(ctx context.Context, q string) (string, error) {
	u.asked = append(u.asked, q)
	return "", u.out(ctx)
}

func (u *timingUI) Form(ctx context.Context, qs []Question) ([]Answer, error) {
	u.forms = append(u.forms, qs)
	return nil, u.out(ctx)
}

// A UI that keeps the time of its questions gets it on ctx, not as a
// deadline: the same time, halving the same way, and the same deny when
// it is out. Under the line of hidden calls too.
func TestAnswerTimeOnCtx(t *testing.T) {
	for _, hide := range []bool{false, true} {
		var (
			a  *Agent
			j  *fakeJournal
			h  *hiderUI
			cw string
		)
		if hide {
			a, j, _, h, cw = hidingAgent(t, asking(2))
		} else {
			var ui *fakeUI
			a, j, _, ui, cw = newAgent(t, asking(2))
			h = &hiderUI{fakeUI: ui}
		}
		tui := &timingUI{hiderUI: h}
		wrapped := 0
		tui.while = func() {
			if _, ok := a.UI.(workUI); ok {
				wrapped++
			}
		}
		a.Policy, a.UI = askPolicy(t), tui
		if err := a.Start(context.Background(), "remove x", tools.Exec{Dir: cw}); err != nil {
			t.Fatal(err)
		}
		if hide && wrapped != 2 {
			t.Errorf("hide_work: %d questions asked under the line of hidden calls", wrapped)
		}
		if want := []time.Duration{10 * mins, 5 * mins}; !reflect.DeepEqual(tui.times, want) || tui.deadlines != 0 {
			t.Errorf("hide_work %v: times %v, want %v; %d deadlines", hide, tui.times, want, tui.deadlines)
		}
		if want := []string{"no answer in 10m", "no answer in 5m"}; !reflect.DeepEqual(tui.whys, want) {
			t.Errorf("hide_work %v: causes %q", hide, tui.whys)
		}
		var results []string
		for _, e := range j.es {
			if e.Kind == session.KindToolResult {
				results = append(results, e.Output)
			}
		}
		if want := []string{"denied by policy: the user did not answer in 10m", "denied by policy: the user did not answer in 5m"}; !reflect.DeepEqual(results, want) {
			t.Errorf("hide_work %v: results %q", hide, results)
		}
	}
}

// So does the form of ask_user: ask_timeout on ctx, and the model told
// nobody answered when it is out.
func TestAnswerTimeForm(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "ask_user", askArgs)}},
		{Text: "ok"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.AskTimeout = "2m"
	tui := &timingUI{hiderUI: &hiderUI{fakeUI: ui}}
	a.UI = tui
	if err := a.Start(context.Background(), "make it nice", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tui.times, []time.Duration{2 * mins}) || tui.deadlines != 0 {
		t.Errorf("times %v, %d deadlines", tui.times, tui.deadlines)
	}
	if r := j.es[2]; r.IsError || r.Output != noAnswer(2*mins) {
		t.Errorf("result %+v", r)
	}

	// ask_timeout "0": no time, the form waits as long as the request.
	prov = &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "ask_user", askArgs)}},
		{Text: "ok"},
	}}
	a, _, _, ui, cwd = newAgent(t, prov)
	a.Cfg.AskTimeout = "0"
	tui = &timingUI{hiderUI: &hiderUI{fakeUI: ui}}
	a.UI = tui
	if err := a.Start(context.Background(), "make it nice", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(tui.forms) != 1 || tui.times != nil || tui.deadlines != 0 {
		t.Errorf("ask_timeout 0: forms %d, times %v, %d deadlines", len(tui.forms), tui.times, tui.deadlines)
	}
}
