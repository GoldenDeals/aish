package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// askArgs are the arguments of an ask_user call: a question to choose
// one option of, and one to check several of.
const askArgs = `{"questions":[
	{"question":"Which approach?","header":"Approach","options":[{"label":"Rewrite","description":"Start from scratch"},{"label":"Patch"}]},
	{"question":"Which features?","header":"Features","multi_select":true,"options":[{"label":"Colors"},{"label":"Icons"},{"label":"Sounds"}]}
]}`

var askQuestions = []Question{
	{Question: "Which approach?", Header: "Approach", Options: []Option{{Label: "Rewrite", Description: "Start from scratch"}, {Label: "Patch"}}},
	{Question: "Which features?", Header: "Features", MultiSelect: true, Options: []Option{{Label: "Colors"}, {Label: "Icons"}, {Label: "Sounds"}}},
}

func decoded(t *testing.T, s string) map[string]any {
	t.Helper()
	args, err := tools.Decode(json.RawMessage(s))
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func TestParseQuestions(t *testing.T) {
	qs, err := ParseQuestions(decoded(t, askArgs))
	if err != nil || !reflect.DeepEqual(qs, askQuestions) {
		t.Errorf("%+v, %v", qs, err)
	}
	// The array as JSON text, as some models send it.
	b, _ := json.Marshal(decoded(t, askArgs)["questions"])
	qs, err = ParseQuestions(map[string]any{"questions": string(b)})
	if err != nil || !reflect.DeepEqual(qs, askQuestions) {
		t.Errorf("as text: %+v, %v", qs, err)
	}

	opts := `"options":[{"label":"a"},{"label":"b"}]`
	q := `{"question":"q?","header":"h",` + opts + `}`
	for _, tc := range []struct{ questions, err string }{
		{``, "questions: 1 to 4 are needed, got 0"},
		{`[]`, "questions: 1 to 4 are needed, got 0"},
		{`"nonsense"`, "questions: invalid character"},
		{`[1]`, "questions: json: cannot unmarshal"},
		{`[` + strings.Repeat(q+",", 4) + q + `]`, "questions: 1 to 4 are needed, got 5"},
		{`[{"question":" ","header":"h",` + opts + `}]`, "question 1: no text"},
		{`[` + q + `,{"question":"q?","options":[{"label":"a"}]}]`, "question 2: 2 to 4 options are needed, got 1"},
		{`[{"question":"q?","options":[{"label":"a"},{"label":"b"},{"label":"c"},{"label":"d"},{"label":"e"}]}]`, "question 1: 2 to 4 options are needed, got 5"},
		{`[{"question":"q?","options":[{"label":"a"},{"description":"no label"}]}]`, "question 1, option 2: no label"},
	} {
		args := `{}`
		if tc.questions != "" {
			args = `{"questions":` + tc.questions + `}`
		}
		if _, err := ParseQuestions(decoded(t, args)); err == nil || !strings.HasPrefix(err.Error(), tc.err) {
			t.Errorf("%s: %v, want %q", args, err, tc.err)
		}
	}
}

func TestAnswerText(t *testing.T) {
	qs := append(askQuestions[:2:2], Question{Question: "Anything else?"})
	ans := []Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Colors", "Sounds"}, Other: "lights"}, {Other: "no"}}
	want := "Approach: Patch\nFeatures: Colors, Sounds, lights\nAnything else?: no"
	if got := answerText(qs, ans); got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if ans[1].Picked[1] != "Sounds" || len(ans[1].Picked) != 2 {
		t.Errorf("String changed the answer: %+v", ans[1])
	}
}

// ask_user is a dialog: offered to the model with its whole schema, but
// neither a command of the user nor a wrapper.
func TestAskUserTool(t *testing.T) {
	tl, ok := tools.Load("").Get("ask_user")
	if !ok || !tools.IsDialog(tl) || tools.Wraps(tl) {
		t.Fatalf("ask_user: %v, dialog %v, wrapped %v", ok, ok && tools.IsDialog(tl), ok && tools.Wraps(tl))
	}
	items := tl.Schema()["properties"].(map[string]any)["questions"].(map[string]any)["items"].(map[string]any)
	if req := items["required"]; !reflect.DeepEqual(req, []string{"question", "header", "options"}) {
		t.Errorf("questions required %v", req)
	}
	if got := tools.Title(tl, decoded(t, askArgs)); got != "ask_user Approach, Features" {
		t.Errorf("title %q", got)
	}
	if _, err := tl.Execute(context.Background(), tools.Exec{}, nil, nil); err == nil {
		t.Error("ask_user ran without the agent")
	}
}

// askAgent runs a request whose model calls ask_user and then answers
// "ok", the form answered by form.
func askAgent(t *testing.T, args string, form func(context.Context, []Question) ([]Answer, error)) (*fakeJournal, *fakeUI, *fakeProvider, error) {
	t.Helper()
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "ask_user", args)}},
		{Text: "ok"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	ui.cols, ui.form = 80, form
	err := a.Start(context.Background(), "make it nice", tools.Exec{Dir: cwd})
	return j, ui, prov, err
}

func TestAskUserCall(t *testing.T) {
	answered := func(context.Context, []Question) ([]Answer, error) {
		return []Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Colors"}, Other: "lights"}}, nil
	}
	j, ui, prov, err := askAgent(t, askArgs, answered)
	if err != nil {
		t.Fatal(err)
	}
	if len(ui.forms) != 1 || !reflect.DeepEqual(ui.forms[0], askQuestions) {
		t.Errorf("asked %+v", ui.forms)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	if r := j.es[2]; r.IsError || r.Output != "Approach: Patch\nFeatures: Colors, lights" {
		t.Errorf("result %+v", r)
	}
	// The form opens below the call: its line is closed, no status for it.
	if out := ui.String(); !strings.Contains(out, "⚙\x1b[0m ask_user Approach, Features\n") || len(ui.at) != 0 || len(ui.folds) != 0 {
		t.Errorf("terminal %q, at %v, folds %v", out, ui.at, ui.folds)
	}
	if len(prov.requests) != 2 || len(prov.requests[1].Messages[2].ToolResults) != 1 {
		t.Error("the answers did not reach the model")
	}
	var offered bool
	for _, d := range prov.requests[0].Tools {
		offered = offered || d.Name == "ask_user"
	}
	if !offered {
		t.Error("ask_user is not offered to the model")
	}
}

// Esc is an error result the model gets, nobody to answer too; a call the
// model got wrong never opens the form.
func TestAskUserNoAnswer(t *testing.T) {
	escaped := func(context.Context, []Question) ([]Answer, error) { return nil, nil }
	for _, tc := range []struct {
		name, args string
		form       func(context.Context, []Question) ([]Answer, error)
		result     string
		shown      string
	}{
		{"cancelled", askArgs, escaped, cancelled, "✗ cancelled"},
		{"no terminal", askArgs, nil, "cannot ask the user: no terminal", "✗ cannot ask the user"},
		{"bad call", `{"questions":[]}`, escaped, "questions: 1 to 4 are needed, got 0", "✗ questions"},
	} {
		j, ui, _, err := askAgent(t, tc.args, tc.form)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if r := j.es[2]; r.Kind != session.KindToolResult || !r.IsError || r.Output != tc.result {
			t.Errorf("%s: result %+v", tc.name, r)
		}
		if !strings.Contains(ui.String(), tc.shown) {
			t.Errorf("%s: terminal %q", tc.name, ui.String())
		}
		if tc.name == "bad call" && len(ui.forms) != 0 {
			t.Errorf("%s: the form opened", tc.name)
		}
	}
}

// Ctrl+C while the form is open ends the request and leaves the call
// pending; the next request closes it.
func TestAskUserInterrupted(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "ask_user", askArgs)}},
		{Text: "ok"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	ui.form = func(ctx context.Context, _ []Question) ([]Answer, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := a.Start(ctx, "make it nice", tools.Exec{Dir: cwd}); err == nil {
		t.Fatal("the interrupted request went on")
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Fatalf("journal %s", got)
	}
	if err := a.Start(context.Background(), "never mind", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2]; r.Kind != session.KindToolResult || r.ToolCallID != "c1" || !r.IsError || r.Output != "interrupted by the user" {
		t.Errorf("closed call %+v", r)
	}
}

// A post-tool hook sees the answers and may replace them, as any result.
func TestAskUserPostTool(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "ask_user", askArgs)}},
		{Text: "ok"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	ui.form = func(context.Context, []Question) ([]Answer, error) {
		return []Answer{{Picked: []string{"Rewrite"}}, {Picked: []string{"Icons"}}}, nil
	}
	a.Cfg.HooksDir = ""
	hook(t, a, "post-tool", "seen", `grep -q '"output":"Approach: Rewrite' && echo '{"output":"Approach: Patch"}'`)
	if err := a.Start(context.Background(), "make it nice", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2]; r.Output != "Approach: Patch" {
		t.Errorf("result %+v", r)
	}
}
