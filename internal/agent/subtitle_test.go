package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// A task's title is its subagent and description, on one line, without
// control characters, a few words at most; a title the call repeats is
// numbered among its own.
func TestTaskTitles(t *testing.T) {
	tt := &taskTool{a: &Agent{subs: []subagent.Def{def("runner"), def("reviewer"), def("x")}}}
	long := strings.Repeat("word ", 20)
	var args map[string]any
	if err := json.Unmarshal([]byte(`{"tasks":[
		{"agent":"runner","prompt":"a"},
		{"agent":"runner","prompt":"b"},
		{"agent":"runner","prompt":"c","description":"  check  the\ndiff\u001b[31m "},
		{"agent":"reviewer","prompt":"d"},
		{"agent":"x","prompt":"e","description":"same"},
		{"agent":"x","prompt":"f","description":"same"},
		{"agent":"reviewer","prompt":"g","description":"`+long+`"},
		{"agent":"reviewer","prompt":"h","description":7}
	]}`), &args); err != nil {
		t.Fatal(err)
	}
	jobs, err := tt.jobs(args)
	if err != nil {
		t.Fatal(err)
	}
	got := taskTitles(jobs)
	want := []string{"runner #1", "runner #2", "runner: check the diff [31m", "reviewer #1", "x: same #1", "x: same #2",
		"reviewer: " + runewidth.Truncate(strings.TrimSpace(long), descMax, "…"), "reviewer #2"}
	if !slices.Equal(got, want) {
		t.Errorf("titles\n%q\nwant\n%q", got, want)
	}
	if w := runewidth.StringWidth(jobs[6].desc); w != descMax {
		t.Errorf("a long description is %d columns", w)
	}
}

// Every task of a call has its pane from the start, those past maxParallel
// queued: the fifth starts once one of the first four is done. A pane is
// titled by the task and given its prompt; the blocks of the result are
// headed by the titles too.
func TestTaskPanesAllAtOnce(t *testing.T) {
	tasks := `[{"agent":"alpha","prompt":"job 1"},{"agent":"alpha","prompt":"job 2"},{"agent":"alpha","prompt":"job 3"},` +
		`{"agent":"alpha","prompt":"job 4"},{"agent":"alpha","prompt":"job 5","description":"check the diff"}]`
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(tasks)(req), onText)
		}
		return reply(&llm.Response{Text: "did " + lastUser(req).Text}, onText)
	}
	a, j, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
	pu := &panesUI{fakeUI: ui}
	a.UI = pu
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	titles := []string{"alpha #1", "alpha #2", "alpha #3", "alpha #4", "alpha: check the diff"}
	var opened []string
	for _, e := range pu.events[:5] {
		opened = append(opened, strings.TrimPrefix(e, "pane "))
	}
	if !slices.Equal(opened, titles) {
		t.Fatalf("not all opened first: %q", pu.events)
	}
	for i, p := range pu.panes {
		if want := "job " + string(rune('1'+i)); p.prompt != want || p.exit == nil || *p.exit != 0 {
			t.Errorf("pane %s: prompt %q, exit %v", p.title, p.prompt, p.exit)
		}
	}
	fifth, done := slices.Index(pu.events, "start alpha: check the diff"), slices.IndexFunc(pu.events, func(e string) bool {
		return strings.HasPrefix(e, "finish ")
	})
	if fifth < 0 || done < 0 || fifth < done {
		t.Errorf("the fifth did not wait its turn: %q", pu.events)
	}
	r := j.es[2].Output
	for _, title := range titles {
		if !strings.Contains(r, "## "+title+" (ok)\n") {
			t.Errorf("no block of %s in %q", title, r)
		}
	}
}

// A pane is told how many calls its subagent made, and that its answer is
// partial when max_steps stopped it: it finishes with 0 all the same.
func TestTaskPanePartial(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		switch subOf(req) {
		case "":
			return reply(host(`[{"agent":"alpha","prompt":"look"},{"agent":"beta","prompt":"look"}]`)(req), onText)
		case "alpha":
			return cluesEachStep(req, onText)
		}
		return reply(&llm.Response{Text: "found it"}, onText)
	}
	a, _, _, ui, cwd := newSubAgent(t, prov, def("alpha"), def("beta"))
	a.Cfg.MaxSteps = 3
	pu := &panesUI{fakeUI: ui}
	a.UI = pu
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	alpha, beta := pu.panes[0], pu.panes[1]
	if !alpha.partial || alpha.calls != 3 || alpha.exit == nil || *alpha.exit != 0 {
		t.Errorf("alpha: partial %v, %d calls, exit %v", alpha.partial, alpha.calls, alpha.exit)
	}
	if beta.partial || beta.calls != 0 {
		t.Errorf("beta: partial %v, %d calls", beta.partial, beta.calls)
	}
}

// Without panes the subagents' sections come one after another on the
// call's output, each headed by its title and its task, dim.
func TestTaskRelayTitles(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"look at\nthe diff","description":"check it"},{"agent":"beta","prompt":"y"}]`)(req), onText)
		}
		return reply(&llm.Response{Text: subOf(req) + " reply\n"}, onText)
	}
	a, _, _, ui, cwd := newSubAgent(t, prov, def("alpha"), def("beta"))
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	out := ui.String()
	for _, want := range []string{
		bold + "── alpha: check it" + reset + "\n" + dim + "> look at\n> the diff" + reset + "\nalpha reply\n",
		bold + "── beta" + reset + "\n" + dim + "> y" + reset + "\nbeta reply\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%q", want, out)
		}
	}
}

// A task in the background keeps its description for aish tasks.
func TestBackgroundTaskDesc(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		return reply(&llm.Response{Text: "ok"}, onText)
	}
	a, _, _, _, _ := newBgAgent(t, prov, def("alpha"))
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x","description":"check it"},{"agent":"alpha","prompt":"y"}],"background":true}`)
	list := a.BackgroundTasks()
	if len(list) != 2 || list[0].Desc != "check it" || list[1].Desc != "" || list[0].Prompt != "x" {
		t.Errorf("tasks %+v", list)
	}
	if got := TaskTitle(list[0].Agent, list[0].Desc); got != "alpha: check it" {
		t.Errorf("title %q", got)
	}
}

// The task above its output, quoted line by line.
func TestQuoteTask(t *testing.T) {
	for in, want := range map[string]string{
		"look\n\n  at it  \n": "> look\n>\n>   at it",
		"\n\none":             "> one",
		" \n\t\n":             "",
		"":                    "",
	} {
		if got := QuoteTask(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
