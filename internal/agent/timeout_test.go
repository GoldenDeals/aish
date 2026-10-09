package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/tools"
)

// timedTool puts an external tool into cwd that prints "early" and the
// arguments it got, then sleeps 10 s, and loads the tools of cwd.
func timedTool(t *testing.T, a *Agent, cwd string) {
	t.Helper()
	script := "#!/bin/sh\n# aish:desc Slow\n# aish:arg note? string A note\necho \"early$*\"\nsleep 10\necho late\n"
	if err := os.WriteFile(filepath.Join(cwd, "slow"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Tools = tools.Load(cwd)
}

// An external tool that runs past the timeout the model gave its call is
// stopped as Esc stops it: the model gets what it printed and the mark of
// the time, and takes its next turn. The tool gets no timeout of aish's
// among its arguments; tool_timeout holds when the call gives none.
func TestToolTimeoutStopsCall(t *testing.T) {
	for _, tc := range []struct {
		name, args, conf, why string
		hide                  bool
	}{
		{"given", `{"timeout":1}`, "2m", "timed out after 1s", false},
		{"given as text", `{"timeout":"0.3"}`, "2m", "timed out after 300ms", false},
		{"tool_timeout", `{}`, "300ms", "timed out after 300ms", false},
		{"tool_timeout, hide_work", `{}`, "300ms", "timed out after 300ms", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &fakeProvider{replies: []*llm.Response{
				{ToolCalls: []llm.ToolCall{toolCall("c1", "slow", tc.args)}},
				{Text: "went on"},
			}}
			var a *Agent
			var j *fakeJournal
			var ui *fakeUI
			var cwd string
			if tc.hide {
				var h *hiderUI
				a, j, _, h, cwd = hidingAgent(t, prov)
				ui = h.fakeUI
			} else {
				a, j, _, ui, cwd = newAgent(t, prov)
			}
			a.Cfg.ToolTimeout = tc.conf
			timedTool(t, a, cwd)
			start := time.Now()
			if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("took %v: the call was not stopped", d)
			}
			if got := kinds(j.es); got != "user assistant tool_result assistant" {
				t.Fatalf("journal %s", got)
			}
			want := "early\n[" + tc.why + "]"
			if r := j.es[2]; r.ToolCallID != "c1" || !r.IsError || r.Output != want {
				t.Errorf("result %+v, want %q", r, want)
			}
			if len(prov.requests) != 2 {
				t.Errorf("%d turns, want the one after the call too", len(prov.requests))
			}
			if !strings.Contains(ui.String(), "("+tc.why+")") {
				t.Errorf("the terminal does not tell why:\n%s", ui.String())
			}
		})
	}
}

// argsTool records the arguments and the context of its calls. Its schema
// is given: with a timeout of its own, it stands for an MCP or external
// tool that takes one.
type argsTool struct {
	name   string
	schema map[string]any
	server string
	sleep  time.Duration
	mu     sync.Mutex
	args   []map[string]any
	limits []bool // whether the call's context had a deadline
}

func (r *argsTool) Name() string           { return r.name }
func (r *argsTool) Desc() string           { return "Records" }
func (r *argsTool) Args() []tools.Arg      { return nil }
func (r *argsTool) Schema() map[string]any { return r.schema }
func (r *argsTool) Server() string         { return r.server }
func (r *argsTool) Execute(ctx context.Context, _ tools.Exec, args map[string]any, _ io.Writer) (string, error) {
	_, limited := ctx.Deadline()
	r.mu.Lock()
	r.args = append(r.args, args)
	r.limits = append(r.limits, limited)
	r.mu.Unlock()
	select {
	case <-time.After(r.sleep):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "ok", nil
}

// A tool with a timeout argument of its own, an MCP tool here, gets it as
// the model gave it, and aish sets its calls no limit, tool_timeout either;
// its schema goes to the model as the server made it.
func TestToolTimeoutOwnArgument(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"q":       map[string]any{"type": "string"},
		"timeout": map[string]any{"type": "integer", "description": "the server's own"},
	}}
	own := &argsTool{name: "srv_search", schema: schema, server: "srv", sleep: 500 * time.Millisecond}
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", own.name, `{"q":"x","timeout":5}`)}},
		{ToolCalls: []llm.ToolCall{toolCall("c2", own.name, `{"q":"y"}`)}},
		{Text: "done"},
	}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Cfg.ToolTimeout = "100ms"
	a.Tools.Add(own)
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	for _, i := range []int{2, 4} {
		if r := j.es[i]; r.Output != "ok" || r.IsError {
			t.Errorf("result %d %+v: the call was cut", i, r)
		}
	}
	if len(own.args) != 2 || own.args[0]["timeout"] != float64(5) || own.args[0]["q"] != "x" {
		t.Errorf("args %v: the tool's own timeout not as given", own.args)
	}
	if _, ok := own.args[1]["timeout"]; ok || own.limits[0] || own.limits[1] {
		t.Errorf("args %v, deadlines %v: aish limited the calls", own.args, own.limits)
	}
	got, _ := json.Marshal(defOf(t, prov.requests[0], own.name).Schema)
	want, _ := json.Marshal(schema)
	if string(got) != string(want) {
		t.Errorf("schema %s, want the server's %s", got, want)
	}
}

func defOf(t *testing.T, req llm.Request, name string) llm.ToolDef {
	t.Helper()
	for _, d := range req.Tools {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no tool %s in the request", name)
	return llm.ToolDef{}
}

// timeoutOf is the description of the timeout argument of def, "" when
// it has none.
func timeoutOf(d llm.ToolDef) string {
	props, _ := d.Schema["properties"].(map[string]any)
	p, _ := props[timeoutArg].(map[string]any)
	desc, _ := p["description"].(string)
	return desc
}

// The model is told of the argument by every tool but ask_user, with the
// default; task's is none.
func TestToolTimeoutSchema(t *testing.T) {
	prov := &subProvider{answer: func(context.Context, llm.Request, func(string)) (*llm.Response, error) {
		return &llm.Response{Text: "done"}, nil
	}}
	a, _, _, _, cwd := newSubAgent(t, prov, def("alpha"))
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	req := prov.all()[0]
	for name, want := range map[string]string{
		tools.Bash:  "exit 124 and its output so far (default 120; 0 for none)",
		"read_file": "its output so far being its result (default 120; 0 for none)",
		subName:     "(default none)",
		taskWait:    "Seconds to wait at most: 60 by default, 600 at most", // its own
		"ask_user":  "",
	} {
		got := timeoutOf(defOf(t, req, name))
		if want == "" && got != "" || !strings.HasSuffix(got, want) {
			t.Errorf("%s: timeout %q, want …%q", name, got, want)
		}
	}
	if s := fmtJSON(defOf(t, req, "read_file").Schema); !strings.Contains(s, `"required":["path"]`) {
		t.Errorf("read_file's schema lost its own: %s", s)
	}
	if _, ok := tools.Builtins()[1].Schema()["properties"].(map[string]any)[timeoutArg]; ok {
		t.Error("the tool's own schema got the argument")
	}
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A timeout that is no number of seconds fails the call before it runs.
func TestToolTimeoutBadValue(t *testing.T) {
	rec := &argsTool{name: "rec", schema: map[string]any{"type": "object"}}
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "rec", `{"timeout":"soon"}`)}},
		{ToolCalls: []llm.ToolCall{toolCall("c2", "rec", `{"timeout":-1}`)}},
		{Text: "done"},
	}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Tools.Add(rec)
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]string{2: "timeout: soon is not a number of seconds", 4: "timeout: -1 is not a number of seconds"} {
		if r := j.es[i]; r.Output != want || !r.IsError {
			t.Errorf("result %d %+v, want %q", i, r, want)
		}
	}
	if len(rec.args) != 0 {
		t.Errorf("ran with %v", rec.args)
	}
}

// The timeout is aish's: neither the tool, nor the policy and the hooks
// see it — the pre-tool hook gets what the policy does — and the call has
// it as its limit.
func TestToolTimeoutNotTheTools(t *testing.T) {
	rec := &argsTool{name: "rec", schema: map[string]any{"type": "object"}}
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "rec", `{"x":"1","timeout":30}`)}},
		{ToolCalls: []llm.ToolCall{toolCall("c2", tools.Bash, `{"command":"ls","timeout":30}`)}},
	}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Tools.Add(rec)
	dir := t.TempDir()
	hook(t, a, "pre-tool", "keep", `cat >>"`+dir+`/pre"`)
	hook(t, a, "post-tool", "keep", `cat >>"`+dir+`/post"`)
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(rec.args) != 1 || len(rec.args[0]) != 1 || rec.args[0]["x"] != "1" || !rec.limits[0] {
		t.Errorf("the tool got %v, limited %v", rec.args, rec.limits)
	}
	a.Shell.(*fakeShell).outputs["c2"] = rpc.Output{Output: "a\n", Cwd: cwd}
	prov.replies = append(prov.replies, &llm.Response{Text: "done"})
	if err := a.Resume(context.Background(), "c2", 0, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	for _, f := range []string{"pre", "post"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) != 2 {
			t.Fatalf("%s hooks ran %d times:\n%s", f, len(lines), b)
		}
		for _, l := range lines {
			var in struct {
				Args map[string]any `json:"args"`
			}
			if err := json.Unmarshal([]byte(l), &in); err != nil {
				t.Fatalf("%s: %v: %s", f, err, l)
			}
			if _, ok := in.Args[timeoutArg]; ok || len(in.Args) != 1 {
				t.Errorf("the %s hook got %v", f, in.Args)
			}
		}
	}
}

// stoppingShell is a fakeShell that can stop the commands it was handed,
// as the proxy does: it records the stops.
type stoppingShell struct {
	*fakeShell
	mu    sync.Mutex
	stops []string // "id code why"
	stop  chan struct{}
}

func (s *stoppingShell) Interrupt(id string, in *Interruption) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stops = append(s.stops, id+" "+strconv.Itoa(in.Code)+" "+in.Why)
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	return true
}

func (s *stoppingShell) stopped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stops...)
}

// A command for the shell is stopped by the shell past its limit: the
// timer is the agent's and outlives the request that handed the command
// off. The output comes back with exit 124 and Why, which the model gets
// after it, and the terminal shows. A command whose output came back in
// time is not stopped after.
func TestToolTimeoutHandOff(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", tools.Bash, `{"command":"sleep 30","timeout":0.2}`)}},
		{ToolCalls: []llm.ToolCall{toolCall("c2", tools.Bash, `{"command":"true"}`)}},
		{Text: "done"},
	}}
	a, j, fake, ui, cwd := newAgent(t, prov)
	a.Cfg.ToolTimeout = "300ms"
	stopped := make(chan struct{})
	sh := &stoppingShell{fakeShell: fake, stop: stopped}
	a.Shell = sh
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	inTime(t, stopped, "the stop of c1")
	if got := sh.stopped(); len(got) != 1 || got[0] != "c1 124 timed out after 200ms" {
		t.Fatalf("stops %q", got)
	}
	fake.outputs["c1"] = rpc.Output{Output: "waiting\n", Exit: 124, Cwd: cwd, Why: "timed out after 200ms"}
	if err := a.Resume(context.Background(), "c1", 124, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	want := "waiting\n[exit 124, cwd " + cwd + "]\n[timed out after 200ms]"
	if r := j.es[2]; r.Output != want {
		t.Errorf("result %q, want %q", r.Output, want)
	}
	if !strings.Contains(ui.String(), "(timed out after 200ms)") {
		t.Errorf("the terminal does not tell why:\n%s", ui.String())
	}
	// c2, handed off now with tool_timeout, is back in time.
	fake.outputs["c2"] = rpc.Output{Cwd: cwd}
	if err := a.Resume(context.Background(), "c2", 0, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if got := sh.stopped(); len(got) != 1 {
		t.Errorf("stops %q: a command back in time was stopped", got)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant tool_result assistant" {
		t.Errorf("journal %s", got)
	}
}

// The bash of a subagent runs past its limit no more: it ends with 124,
// and the subagent's model gets the mark after what it printed.
func TestToolTimeoutSubagentBash(t *testing.T) {
	var mu sync.Mutex
	var result string
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"run"}]`)(req), onText)
		}
		if rs := lastUser(req).ToolResults; len(rs) > 0 {
			mu.Lock()
			result = rs[0].Content
			mu.Unlock()
			return reply(&llm.Response{Text: "alpha ran it"}, onText)
		}
		return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("b1", tools.Bash, `{"command":"echo hi; sleep 10","timeout":0.3}`)}}, nil
	}
	a, j, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
	start := time.Now()
	if err := a.Start(context.Background(), "run", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v: the command was not stopped", d)
	}
	want := "hi\n[exit 124, cwd " + cwd + "]\n[timed out after 300ms]"
	if result != want {
		t.Errorf("the subagent got %q, want %q", result, want)
	}
	if s := ui.String(); !strings.Contains(s, "exit 124") || !strings.Contains(s, "(timed out after 300ms)") {
		t.Errorf("the terminal:\n%s", s)
	}
	if r := j.es[2].Output; r != "## alpha (ok)\nalpha ran it" {
		t.Errorf("task's result %q", r)
	}
}

// A subagent's command stopped before it started does not start.
func TestSubShellStopBeforeRun(t *testing.T) {
	sh := &subShell{}
	if sh.Interrupt("b1", ByUser) {
		t.Error("stopped a command never handed")
	}
	_ = sh.HandOff("b1", "touch x")
	if !sh.Interrupt("b1", overTime(time.Second)) {
		t.Fatal("did not stop the command handed")
	}
	id, _, _ := sh.take()
	ran := false
	o := sh.run(context.Background(), id, "/d", func(context.Context) rpc.Output { ran = true; return rpc.Output{} })
	if ran || o.Exit != 124 || o.Why != "timed out after 1s" || o.Cwd != "/d" {
		t.Errorf("ran %v, output %+v", ran, o)
	}
	if sh.Interrupt("b1", ByUser) {
		t.Error("stopped a command that is over")
	}
}

// task runs past tool_timeout: its subagents are made to work long. A
// timeout given to the call stops it with them.
func TestToolTimeoutTask(t *testing.T) {
	for _, tc := range []struct {
		name, args, want string
	}{
		{"tool_timeout", `{"tasks":[{"agent":"alpha","prompt":"x"}]}`, "## alpha (ok)\nalpha reply"},
		{"given", `{"tasks":[{"agent":"alpha","prompt":"x"}],"timeout":0.1}`, "[timed out after 100ms]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &subProvider{}
			prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
				if subOf(req) == "" {
					return reply(hostTurnFor(req, tc.args), onText)
				}
				select {
				case <-time.After(400 * time.Millisecond):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return reply(&llm.Response{Text: "alpha reply"}, onText)
			}
			a, j, _, _, cwd := newSubAgent(t, prov, def("alpha"))
			a.Cfg.ToolTimeout = "100ms"
			if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
				t.Fatal(err)
			}
			if r := j.es[2].Output; r != tc.want {
				t.Errorf("result %q, want %q", r, tc.want)
			}
		})
	}
}

// hostTurnFor is the host's turn: the call of task with args, then "done"
// once its result is in.
func hostTurnFor(req llm.Request, args string) *llm.Response {
	if len(lastUser(req).ToolResults) > 0 {
		return &llm.Response{Text: "done"}
	}
	return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("t1", subName, args)}}
}

// A timeout given to task limits its subagents in the background too,
// which past it end with an error that says so; without one they work on.
func TestToolTimeoutBackground(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	prov := gatedSubs(hostTurns(
		callOf("t1", subName, `{"tasks":[{"agent":"alpha","prompt":"A"}],"background":true,"timeout":0.2}`),
		&llm.Response{Text: "started"},
		callOf("t2", subName, `{"tasks":[{"agent":"beta","prompt":"B"}],"background":true}`),
		&llm.Response{Text: "started"},
	), map[string]chan struct{}{"alpha": gate, "beta": gate})
	a, _, _, _, cwd := newBgAgent(t, prov, def("alpha"), def("beta"))
	a.Cfg.ToolTimeout = "100ms"
	for range 2 {
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := mustUse(t, a, taskResult, `{"ids":["bg1","bg2"]}`)
		if strings.Contains(got, "## bg1 alpha (error)\ntimed out after 200ms") {
			if !strings.Contains(got, "## bg2 beta (running)") {
				t.Errorf("tool_timeout stopped one in the background:\n%s", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task_result:\n%s", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
