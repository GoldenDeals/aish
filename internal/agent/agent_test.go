package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// fakeProvider answers each Complete with the next scripted response.
type fakeProvider struct {
	replies  []*llm.Response
	requests []llm.Request
	// before runs at each Complete, to stream text or cancel the context.
	before func(ctx context.Context, n int, onText func(string))
}

func (f *fakeProvider) Name() string           { return "fake" }
func (f *fakeProvider) Model() string          { return "m" }
func (f *fakeProvider) Efforts() []string      { return nil }
func (f *fakeProvider) MaxTokens(string) int64 { return 0 }
func (f *fakeProvider) Models(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func (f *fakeProvider) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	n := len(f.requests)
	f.requests = append(f.requests, req)
	if f.before != nil {
		f.before(ctx, n, onText)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n >= len(f.replies) {
		return nil, errors.New("no reply scripted")
	}
	r := f.replies[n]
	if onText != nil && r.Text != "" {
		onText(r.Text)
	}
	return r, nil
}

func toolCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

// fakeJournal is a journal in memory.
type fakeJournal struct {
	id string
	es []session.Entry
}

func (j *fakeJournal) ID() string               { return j.id }
func (j *fakeJournal) Len() int                 { return len(j.es) }
func (j *fakeJournal) Entries() []session.Entry { return append([]session.Entry(nil), j.es...) }
func (j *fakeJournal) Append(es ...session.Entry) error {
	j.es = append(j.es, es...)
	return nil
}

// fakeShell records hand-offs and answers Wait from outputs.
type fakeShell struct {
	handed  []string // "id\x00cmd"
	outputs map[string]rpc.Output
}

func (s *fakeShell) HandOff(id, cmd string) error {
	s.handed = append(s.handed, id+"\x00"+cmd)
	return nil
}

func (s *fakeShell) Wait(_ context.Context, id string, _ time.Duration) (rpc.Output, error) {
	out, ok := s.outputs[id]
	if !ok {
		return rpc.Output{}, errors.New("no output")
	}
	return out, nil
}

// fakeUI is a terminal in a buffer.
type fakeUI struct {
	bytes.Buffer
	cols   int
	answer string // to Ask; "" means nobody can answer
	onAsk  func() // runs while the question is open
	asked  []string
	folds  []string
	lives  []string
	at     []int
	// form answers Form; nil means nobody can answer.
	form  func(ctx context.Context, qs []Question) ([]Answer, error)
	forms [][]Question
}

func (u *fakeUI) Size() (int, int) { return u.cols, 24 }
func (u *fakeUI) Ask(_ context.Context, q string) (string, error) {
	u.asked = append(u.asked, q)
	if u.onAsk != nil {
		u.onAsk()
	}
	if u.answer == "" {
		return "", errors.New("no terminal")
	}
	return u.answer, nil
}
func (u *fakeUI) Form(ctx context.Context, qs []Question) ([]Answer, error) {
	u.forms = append(u.forms, qs)
	if u.form == nil {
		return nil, errors.New("no terminal")
	}
	return u.form(ctx, qs)
}
func (u *fakeUI) Fold(title, text string) { u.folds = append(u.folds, title) }
func (u *fakeUI) Live(title string) Live {
	u.lives = append(u.lives, title)
	return &fakeLive{u: u}
}
func (u *fakeUI) CommandAt(col int, long bool, hidden int) { u.at = append(u.at, col) }

type fakeLive struct{ u *fakeUI }

func (l *fakeLive) Write(b []byte) (int, error) { return l.u.Write(b) }
func (l *fakeLive) Finish(exit int)             {}

// newAgent is an agent over fakes in a home of its own, so that no
// instruction file of the machine running the tests is read.
func newAgent(t *testing.T, prov *fakeProvider) (*Agent, *fakeJournal, *fakeShell, *fakeUI, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cwd := filepath.Join(home, "work")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	j := &fakeJournal{id: "s1"}
	sh := &fakeShell{outputs: map[string]rpc.Output{}}
	ui := &fakeUI{}
	cfg := config.Default()
	cfg.Markdown = false
	a := &Agent{
		Cfg: cfg, Provider: prov, Tools: tools.Load(""), Policy: &policy.Engine{},
		Journal: j, Shell: sh, UI: ui,
	}
	return a, j, sh, ui, cwd
}

func kinds(es []session.Entry) string {
	var ks []string
	for _, e := range es {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, " ")
}

// A request that reads a file and answers: the tool runs in the shell's
// directory, the journal gets every step, the terminal the answer.
func TestStartWithTool(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: "let me look", ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"notes.txt"}`)}},
		{Text: "it says hello"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "what do the notes say", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	if j.es[0].Cwd != cwd {
		t.Errorf("request cwd %q", j.es[0].Cwd)
	}
	if r := j.es[2]; r.ToolCallID != "c1" || r.IsError || !strings.Contains(r.Output, "hello") {
		t.Errorf("tool result %+v", r)
	}
	out := ui.String()
	if !strings.Contains(out, "⚙\x1b[0m read_file notes.txt") || !strings.Contains(out, "it says hello") {
		t.Errorf("terminal:\n%s", out)
	}
	if len(ui.lives) != 0 {
		t.Errorf("a built-in got a live fold: %v", ui.lives)
	}
	// The second turn carried the result back to the model.
	if len(prov.requests) != 2 || len(prov.requests[1].Messages) != 3 || len(prov.requests[1].Messages[2].ToolResults) != 1 {
		t.Errorf("requests %d, last with %d messages", len(prov.requests), len(prov.requests[len(prov.requests)-1].Messages))
	}
	if !strings.Contains(prov.requests[0].System, "# Environment") {
		t.Errorf("system prompt without the environment")
	}
}

// A bash command goes to the shell; Resume brings its output back and the
// request goes on from the entries kept in memory.
func TestBashHandOffAndResume(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"ls"}`)}},
		{Text: "two files"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	ui.cols = 80
	if err := a.Start(context.Background(), "list files", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(sh.handed) != 1 || sh.handed[0] != "c1\x00ls" {
		t.Fatalf("handed off %q", sh.handed)
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Fatalf("journal after hand-off: %s", got)
	}
	if !strings.Contains(ui.String(), "❯\x1b[0m \x1b[1mls") || len(ui.at) != 1 || ui.at[0] != 4 {
		t.Errorf("command not shown with its column: %q, at %v", ui.String(), ui.at)
	}

	// The journal is not read again on resume: a change to it would show.
	j.es = append([]session.Entry(nil), j.es...)
	sh.outputs["c1"] = rpc.Output{Output: "a\nb\n", Exit: 0, Cwd: cwd}
	if err := a.Resume(context.Background(), "c1", 0, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant" {
		t.Fatalf("journal after resume: %s", got)
	}
	if r := j.es[2]; !strings.HasPrefix(r.Output, "a\nb\n") || !strings.HasSuffix(r.Output, "[exit 0, cwd "+cwd+"]") {
		t.Errorf("bash result %q", r.Output)
	}
	if err := a.Resume(context.Background(), "c9", 0, tools.Exec{Dir: cwd}); err == nil || !strings.Contains(err.Error(), "no pending tool call") {
		t.Errorf("resume of an unknown call: %v", err)
	}
}

// Resume reads the journal again when it is not the one the entries came
// from: `clear` or `aish resume` replaced it under the agent.
func TestResumeReloadsAnotherJournal(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"ls"}`)}},
	}}
	a, j, sh, _, cwd := newAgent(t, prov)
	if err := a.Start(context.Background(), "list", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	j.id, j.es = "s2", nil
	sh.outputs["c1"] = rpc.Output{Output: "x", Cwd: cwd}
	if err := a.Resume(context.Background(), "c1", 0, tools.Exec{Dir: cwd}); err == nil {
		t.Fatal("the call of a journal that is gone was resumed")
	}
	if len(j.es) != 0 {
		t.Errorf("wrote into the new journal: %s", kinds(j.es))
	}
}

// Calls the last request left without a result get one before the next.
func TestClosePending(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "ok"}}}
	call := session.ToolCall{ID: "c0", Name: "bash", Args: json.RawMessage(`{"command":"sleep 9"}`)}
	a, j, sh, _, cwd := newAgent(t, prov)
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "wait", Cwd: cwd},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{call}},
	}
	sh.outputs["c0"] = rpc.Output{Output: "partial", Exit: 130, Cwd: cwd}
	if err := a.Start(context.Background(), "next", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result user assistant" {
		t.Fatalf("journal %s", got)
	}
	if r := j.es[2]; r.ToolCallID != "c0" || !r.IsError || !strings.Contains(r.Output, "partial") || !strings.Contains(r.Output, "interrupted by the user") {
		t.Errorf("closed call %+v", r)
	}
}

// The user decides an "ask" verdict on the terminal; Ctrl+C while the
// question is open leaves the call pending.
func TestAsk(t *testing.T) {
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n@ask(\"sure?\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		answer string
		handed int
		result string
	}{
		{"y", 1, ""},
		{"да", 1, ""},
		{"n", 0, "denied by policy: the user declined"},
		{"", 0, "denied by policy: needs confirmation, no terminal: sure?"},
	} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"rm x"}`)}},
			{Text: "fine"},
		}}
		a, j, sh, ui, cwd := newAgent(t, prov)
		a.Policy, ui.answer = pol, tc.answer
		if err := a.Start(context.Background(), "remove x", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		// The choices are the terminal's to draw.
		if len(ui.asked) != 1 || ui.asked[0] != bold+"sure? — allow?"+reset {
			t.Errorf("%q: asked %q", tc.answer, ui.asked)
		}
		if len(sh.handed) != tc.handed {
			t.Errorf("%q: handed off %q", tc.answer, sh.handed)
		}
		if tc.result != "" {
			if r := j.es[len(j.es)-2]; r.Kind != session.KindToolResult || r.Output != tc.result || !r.IsError {
				t.Errorf("%q: result %+v", tc.answer, r)
			}
		}
	}

	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"rm x"}`)}},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Policy = pol
	ctx, cancel := context.WithCancel(context.Background())
	ui.answer, ui.onAsk = "y", cancel // Ctrl+C while the question is open
	if err := a.Start(ctx, "remove x", tools.Exec{Dir: cwd}); !errors.Is(err, context.Canceled) {
		t.Errorf("interrupted ask: %v", err)
	}
	if len(sh.handed) != 0 || kinds(j.es) != "user assistant" {
		t.Errorf("after the interrupted ask: handed %q, journal %s", sh.handed, kinds(j.es))
	}
}

// Ctrl+C in the middle of a reply keeps what the user saw.
func TestInterruptedTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &fakeProvider{before: func(_ context.Context, _ int, onText func(string)) {
		onText("half an ans")
		cancel()
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	if err := a.Start(ctx, "tell me", tools.Exec{Dir: cwd}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Fatalf("journal %s", got)
	}
	if e := j.es[1]; e.Text != "half an ans\n[interrupted by the user]" {
		t.Errorf("kept %q", e.Text)
	}
	if !strings.Contains(ui.String(), "half an ans") {
		t.Errorf("terminal %q", ui.String())
	}
}

// An external tool prints live into a fold; a built-in's long result is
// kept for Ctrl+O.
func TestToolOutputs(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "probe", `{}`), toolCall("c2", "read_file", `{"path":"long.txt"}`)}},
		{Text: "done"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	probe := filepath.Join(cwd, "probe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\n# aish:desc Probe\necho live-from-$PWD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "long.txt"), []byte("1\n2\n3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Tools = tools.Load(cwd)
	if err := a.Start(context.Background(), "probe", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(ui.lives) != 1 || ui.lives[0] != "⚙ probe" {
		t.Errorf("live folds %q", ui.lives)
	}
	if len(ui.folds) != 1 || ui.folds[0] != "⚙ read_file long.txt" {
		t.Errorf("folds %q", ui.folds)
	}
	out := ui.String()
	real, _ := filepath.EvalSymlinks(cwd)
	if !strings.Contains(out, "live-from-"+cwd) && !strings.Contains(out, "live-from-"+real) {
		t.Errorf("external tool did not run in the shell's directory:\n%s", out)
	}
	if strings.Contains(out, "ctrl+o") {
		t.Errorf("the status of the long result is the UI's to draw:\n%s", out)
	}
	if got := kinds(j.es); got != "user assistant tool_result tool_result assistant" {
		t.Errorf("journal %s", got)
	}
}

func TestCompact(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "we talked"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "hi", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "hello"},
	}
	if err := a.Compact(context.Background(), "the greeting", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant summary" || j.es[2].Text != "we talked" {
		t.Fatalf("journal %s: %+v", got, j.es)
	}
	last := prov.requests[0].Messages[len(prov.requests[0].Messages)-1]
	if !strings.Contains(last.Text, "focus on: the greeting") {
		t.Errorf("focus not asked for: %q", last.Text)
	}
	if !strings.Contains(ui.String(), "compacted:") {
		t.Errorf("terminal %q", ui.String())
	}
	if err := a.Compact(context.Background(), "", tools.Exec{Dir: cwd}); err == nil || err.Error() != "nothing to compact" {
		t.Errorf("a summary alone: %v", err)
	}
}
