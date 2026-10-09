package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// partialNote is the line a subagent's answer ends with at 3 steps.
const partialNote = "[stopped after 3 steps: the answer is partial]"

// cluesEachStep is a subagent that, at each step, tells what it has found
// so far and runs one more command: it never gives a final answer.
func cluesEachStep(req llm.Request, onText func(string)) (*llm.Response, error) {
	n := 0
	for _, m := range req.Messages {
		n += len(m.ToolResults)
	}
	id := fmt.Sprintf("b%d", n+1)
	return reply(&llm.Response{
		Text:      fmt.Sprintf("found clue %d so far", n+1),
		ToolCalls: []llm.ToolCall{toolCall(id, tools.Bash, `{"command":"echo hi"}`)},
	}, onText)
}

// A subagent stopped at max_steps answers with the last text it wrote, said
// to be partial, not with drive's note alone: what it found is not lost,
// and the host does not take it for work done.
func TestSubagentMaxStepsPartial(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"look"}]`)(req), onText)
		}
		return cluesEachStep(req, onText)
	}
	a, j, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
	a.Cfg.MaxSteps = 3
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	want := "## alpha (partial)\nfound clue 3 so far\n\n" + partialNote
	if r := j.es[2]; r.Kind != session.KindToolResult || r.IsError || r.Output != want {
		t.Errorf("result %q, want %q", r.Output, want)
	}
	out := ui.String()
	if !strings.Contains(out, "[aish: stopped after 3 steps: the answer is partial]") || strings.Contains(out, "ask to continue") {
		t.Errorf("the subagent's output:\n%s", out)
	}
}

// The answer of a subagent stopped at max_steps is its last text with
// something in it; with none, the note alone. A model's reply that happens
// to read like drive's note is the answer.
func TestSubAnswer(t *testing.T) {
	asst := func(text string) session.Entry {
		return session.Entry{Kind: session.KindAssistant, Text: text, Provider: "fake"}
	}
	note := session.Entry{Kind: session.KindAssistant, Text: stoppedNote(3)}
	result := session.Entry{Kind: session.KindToolResult, Output: "hi"}
	for _, tc := range []struct {
		name    string
		es      []session.Entry
		reply   string
		partial bool
	}{
		{"final", []session.Entry{asst("a"), result, asst(" done ")}, "done", false},
		{"none", nil, "", false},
		{"stopped", []session.Entry{asst("clue 1"), result, asst("clue 2"), result, asst(" "), result, note},
			"clue 2\n\n" + partialNote, true},
		{"stopped silent", []session.Entry{asst(""), result, note}, partialNote, true},
		{"model says it", []session.Entry{asst("x"), result, asst(stoppedNote(3))}, stoppedNote(3), false},
	} {
		reply, partial := subAnswer(tc.es, 3, 0)
		if reply != tc.reply || partial != tc.partial {
			t.Errorf("%s: %q, partial %v; want %q, %v", tc.name, reply, partial, tc.reply, tc.partial)
		}
	}
}

// A subagent in the background stopped at max_steps is partial for aish
// tasks and in its block, as one task waits for.
func TestBackgroundMaxStepsPartial(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		return cluesEachStep(req, onText)
	}
	a, _, _, _, _ := newBgAgent(t, prov, def("alpha"))
	a.Cfg.MaxSteps = 3
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"look"}],"background":true}`)
	want := "## bg1 alpha (partial)\nfound clue 3 so far\n\n" + partialNote
	if got := mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":10}`); got != want {
		t.Errorf("task_wait: %q, want %q", got, want)
	}
	if tasks := a.BackgroundTasks(); len(tasks) != 1 || tasks[0].State != "partial" {
		t.Errorf("aish tasks: %+v", tasks)
	}
	task, err := a.BackgroundTask("bg1")
	if err != nil || task.State != "partial" || !strings.Contains(task.Output, "stopped after 3 steps") {
		t.Errorf("aish tasks show: %+v, %v", task, err)
	}
	if got := mustUse(t, a, taskResult, `{}`); got != "bg1 alpha: partial" {
		t.Errorf("task_result: %q", got)
	}
}

// A user-prompt hook that refuses a subagent's brief fails the subagent:
// the host gets the refusal as an error, not "(no reply)" that passes for
// done, and the subagent's output tells it once.
func TestSubagentHookDenied(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) != "" {
			t.Error("a refused brief reached the model")
		}
		return reply(host(`[{"agent":"alpha","prompt":"BADWORD job"}]`)(req), onText)
	}
	a, j, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
	a.Cfg.HooksDir = ""
	hook(t, a, "user-prompt", "deny", `grep -q BADWORD && echo '{"deny":"no bad words"}'; exit 0`)
	if err := a.Start(context.Background(), "have alpha do it", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2]; r.Output != "## alpha (error)\ndenied by hook deny: no bad words" {
		t.Errorf("result %q", r.Output)
	}
	if out := ui.String(); strings.Count(out, "✗ denied by hook deny: no bad words") != 1 {
		t.Errorf("the subagent's output:\n%s", out)
	}
}

// errSDKRejected is an API error as the SDK gives it: its text has the URL,
// the request id and the whole body.
func errSDKRejected() error {
	e := &anthropic.Error{}
	_ = e.UnmarshalJSON([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"unknown provider for model fable"},"request_id":"req_0117"}`))
	e.StatusCode = 400
	e.Request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8317/v1/messages", nil)
	e.Response = &http.Response{StatusCode: 400}
	return e
}

// A subagent the API fails is told to the host and on its pane as the
// host's own error is told to the user: without the URL and the body.
func TestSubagentAPIErrorShort(t *testing.T) {
	sdk := errSDKRejected()
	if !strings.Contains(sdk.Error(), "127.0.0.1") {
		t.Fatalf("the SDK's error has no URL: %v", sdk)
	}
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"x"}]`)(req), onText)
		}
		return nil, sdk
	}
	a, j, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
	pu := &panesUI{fakeUI: ui}
	a.UI = pu
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	short := llm.Short(sdk)
	if r := j.es[2]; r.Output != "## alpha (error)\n"+short {
		t.Errorf("result %q", r.Output)
	}
	if len(pu.panes) != 1 {
		t.Fatalf("panes %v", pu.panes)
	}
	pane := pu.panes[0].out.String()
	if !strings.Contains(pane, "✗ "+short) {
		t.Errorf("pane %q", pane)
	}
	for _, s := range []string{"127.0.0.1", "req_0117", `"type":"error"`} {
		if strings.Contains(j.es[2].Output, s) || strings.Contains(pane, s) {
			t.Errorf("%s in the result %q or the pane %q", s, j.es[2].Output, pane)
		}
	}
}

// A subagent in the background the API fails is told the same way in aish
// tasks show and in its block.
func TestBackgroundAPIErrorShort(t *testing.T) {
	sdk := errSDKRejected()
	prov := &subProvider{}
	prov.answer = func(context.Context, llm.Request, func(string)) (*llm.Response, error) { return nil, sdk }
	a, _, _, _, _ := newBgAgent(t, prov, def("alpha"))
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"}],"background":true}`)
	got := mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":10}`)
	if got != "## bg1 alpha (error)\n"+llm.Short(sdk) {
		t.Errorf("task_wait: %q", got)
	}
	task, err := a.BackgroundTask("bg1")
	if err != nil || !strings.Contains(task.Output, "✗ "+llm.Short(sdk)) || strings.Contains(task.Output, "127.0.0.1") {
		t.Errorf("aish tasks show: %+v, %v", task, err)
	}
}

// An ask verdict on a subagent's call is a deny that says why: the
// subagent cannot ask the user, though there is a terminal.
func TestSubagentAskReason(t *testing.T) {
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n@ask(\"sure?\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var result string
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"touch it"}]`)(req), onText)
		}
		if rs := lastUser(req).ToolResults; len(rs) > 0 {
			mu.Lock()
			result = rs[0].Content
			mu.Unlock()
			return reply(&llm.Response{Text: "could not"}, onText)
		}
		return callOf("b1", tools.Bash, `{"command":"touch marker"}`), nil
	}
	a, _, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
	a.Policy, ui.answer = pol, "y" // the host's user would say yes
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	want := "denied by policy: needs the user's confirmation, which a subagent cannot ask: sure?"
	if result != want {
		t.Errorf("result %q, want %q", result, want)
	}
	if len(ui.asked) != 0 {
		t.Errorf("the host's terminal was asked: %q", ui.asked)
	}
	if _, err := os.Stat(filepath.Join(cwd, "marker")); err == nil {
		t.Error("the command ran")
	}
}
