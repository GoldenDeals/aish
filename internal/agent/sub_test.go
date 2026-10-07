package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// subProvider answers each request by what it is: answer looks at the
// system prompt and the last user message. Subagents ask it from several
// goroutines at once.
type subProvider struct {
	mu       sync.Mutex
	requests []llm.Request
	answer   func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error)
}

func (p *subProvider) Name() string           { return "fake" }
func (p *subProvider) Model() string          { return "m" }
func (p *subProvider) Efforts() []string      { return nil }
func (p *subProvider) MaxTokens(string) int64 { return 0 }
func (p *subProvider) Models(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func (p *subProvider) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return p.answer(ctx, req, onText)
}

func (p *subProvider) all() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests)
}

// subOf is the subagent a request is from, by the body of its file in
// the system prompt; "" for the host's.
func subOf(req llm.Request) string {
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if strings.Contains(req.System, "BODY-"+name) {
			return name
		}
	}
	return ""
}

func lastUser(req llm.Request) llm.Message {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return req.Messages[i]
		}
	}
	return llm.Message{}
}

func toolNames(req llm.Request) []string {
	var out []string
	for _, t := range req.Tools {
		out = append(out, t.Name)
	}
	return out
}

func def(name string, tools ...string) subagent.Def {
	return subagent.Def{Name: name, Desc: "Does " + name, Prompt: "You are BODY-" + name + ".", Tools: tools}
}

// host answers the host's requests: first the call of task with tasks,
// then "done" once the result is in.
func host(tasks string) func(llm.Request) *llm.Response {
	return func(req llm.Request) *llm.Response {
		if len(lastUser(req).ToolResults) > 0 {
			return &llm.Response{Text: "done"}
		}
		return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("t1", subName, `{"tasks":`+tasks+`}`)}}
	}
}

// reply streams the text of r, as a provider does.
func reply(r *llm.Response, onText func(string)) (*llm.Response, error) {
	if onText != nil && r.Text != "" {
		onText(r.Text)
	}
	return r, nil
}

func newSubAgent(t *testing.T, prov *subProvider, defs ...subagent.Def) (*Agent, *fakeJournal, *fakeShell, *fakeUI, string) {
	t.Helper()
	a, j, sh, ui, cwd := newAgent(t, nil)
	a.Provider = prov
	a.AddSubagents(defs)
	return a, j, sh, ui, cwd
}

// Two subagents of one call run at once, each with its own system prompt,
// and their answers come back in the order of the call; the session gets
// nothing of their work but the result.
func TestTaskRunsSubagentsInParallel(t *testing.T) {
	var started atomic.Int32
	both, betaShown := make(chan struct{}), make(chan struct{})
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		name := subOf(req)
		if name == "" {
			return reply(host(`[{"agent":"alpha","prompt":"job A"},{"agent":"beta","prompt":"job B"}]`)(req), onText)
		}
		if started.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
		case <-time.After(2 * time.Second):
			return nil, errors.New("the other subagent did not start meanwhile")
		}
		if name == "beta" {
			defer close(betaShown)
			return reply(&llm.Response{Text: "beta reply"}, onText)
		}
		// alpha answers after beta: beta's output waits on the screen
		// for alpha's to end.
		select {
		case <-betaShown:
		case <-time.After(2 * time.Second):
		}
		return reply(&llm.Response{Text: "alpha reply"}, onText)
	}
	a, j, sh, ui, cwd := newSubAgent(t, prov, def("alpha"), def("beta"))
	if err := a.Start(context.Background(), "do both", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	want := "## alpha (ok)\nalpha reply\n\n## beta (ok)\nbeta reply"
	if r := j.es[2]; r.Output != want || r.IsError {
		t.Errorf("result %q, error %v; want %q", r.Output, r.IsError, want)
	}
	if len(sh.handed) != 0 {
		t.Errorf("handed off to the live shell: %q", sh.handed)
	}
	if len(ui.lives) != 1 || ui.lives[0] != "⚙ task alpha, beta" {
		t.Errorf("live folds %q", ui.lives)
	}
	// Without panes the outputs follow one another, in the order of the call.
	out := ui.String()
	order := []string{"── alpha", "alpha reply", "── beta", "beta reply"}
	for i := 1; i < len(order); i++ {
		if x, y := strings.Index(out, order[i-1]), strings.Index(out, order[i]); x < 0 || y < x {
			t.Errorf("%q not before %q:\n%s", order[i-1], order[i], out)
		}
	}
	for _, req := range prov.all() {
		names := toolNames(req)
		switch sub := subOf(req); sub {
		case "":
			if strings.Contains(req.System, "BODY-") || strings.Contains(req.System, "# Subagent") {
				t.Errorf("a subagent's prompt in the host's request")
			}
			if !slices.Contains(names, subName) {
				t.Errorf("the host has no task tool: %v", names)
			}
		default:
			if !strings.Contains(req.System, "You are BODY-"+sub+".") {
				t.Errorf("%s: system prompt without its file's body", sub)
			}
			if slices.Contains(names, subName) || slices.Contains(names, "ask_user") {
				t.Errorf("%s got %v", sub, names)
			}
			if !strings.Contains(lastUser(req).Text, "job "+strings.ToUpper(sub[:1])) {
				t.Errorf("%s got the request %q", sub, lastUser(req).Text)
			}
		}
	}
}

// tools: Read leaves a subagent read_file and nothing else.
func TestSubagentToolsFromItsFile(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"gamma","prompt":"read"}]`)(req), onText)
		}
		return reply(&llm.Response{Text: "read it"}, onText)
	}
	a, _, _, _, cwd := newSubAgent(t, prov, def("gamma", "Read"))
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, req := range prov.all() {
		if subOf(req) == "gamma" {
			seen = true
			if got := toolNames(req); !slices.Equal(got, []string{"read_file"}) {
				t.Errorf("gamma's tools %v", got)
			}
		}
	}
	if !seen {
		t.Fatal("gamma never ran")
	}
}

func TestSubTools(t *testing.T) {
	host := tools.Load("")
	a := &Agent{Tools: host}
	a.AddSubagents([]subagent.Def{def("alpha")})
	names := func(r *tools.Registry) string {
		var out []string
		for _, t := range r.All() {
			out = append(out, t.Name())
		}
		return strings.Join(out, " ")
	}
	ro := []string{"cat *", "find *", "grep *", "head *", "ls *", "rg *", "tail *", "wc *"}
	for _, tc := range []struct {
		names []string
		tools string
		scope []string
	}{
		{nil, "bash read_file write_file edit_file", nil},
		{[]string{"Read", "Edit"}, "read_file edit_file", nil},
		{[]string{"MultiEdit", "write_file"}, "write_file edit_file", nil},
		{[]string{"Grep"}, "bash", ro},
		{[]string{"Task", "Agent", "ask_user", "WebFetch"}, "", nil},
		{[]string{"Bash(git diff:*)", "Bash(git log *)"}, "bash", []string{"git diff:*", "git log *"}},
		{[]string{"Glob", "Bash(git *)"}, "bash", append([]string{"git *"}, ro...)},
		{[]string{"Bash(*)"}, "bash", nil},
		{[]string{"Read(src/**)"}, "", nil},
	} {
		reg, scope := subTools(a.Tools, tc.names)
		var pats []string
		if scope != nil {
			pats = strings.Split(scope.String(), ", ")
		}
		if got := names(reg); got != tc.tools || !slices.Equal(pats, tc.scope) {
			t.Errorf("%q: tools %q scope %q, want %q %q", tc.names, got, pats, tc.tools, tc.scope)
		}
	}
	reg, _ := subTools(a.Tools, nil)
	if b, _ := reg.Get(tools.Bash); !strings.Contains(b.Desc(), "does not run in the user's live shell") {
		t.Errorf("a subagent's bash is described as the live shell: %q", b.Desc())
	}
}

func TestMatchCommand(t *testing.T) {
	scope := &bashScope{patterns: []string{"git diff:*", "git log *", "go test ./..."}}
	for cmd, ok := range map[string]bool{
		"git diff":                       true,
		"git diff --stat HEAD~1":         true,
		"git log --oneline -3":           true,
		"git log":                        true,
		"go test ./...":                  true,
		"go test ./... -run X":           false,
		"git diff && git log":            true,
		"git diff | rm -rf x":            false,
		"git diff $(touch y)":            false,
		"bash -c 'git log; touch y'":     false,
		"git differ":                     false,
		"git log 'a\nb'":                 true,
		"timeout 5 git log":              false,
		"git push --force":               false,
		"git diff; X=1 sh -c 'git diff'": false,
	} {
		if got := refused(scope, cmd, "/", nil) == ""; got != ok {
			t.Errorf("%q: allowed %v, want %v (%s)", cmd, got, ok, refused(scope, cmd, "/", nil))
		}
	}
	if refused(nil, "rm -rf x", "/", nil) != "" {
		t.Error("no scope refused a command")
	}
}

// A subagent's bash runs as a process of its own: the live shell gets
// nothing, the subagent gets the output. A command outside its Bash(...)
// entries does not run.
func TestSubagentBashRunsAsProcess(t *testing.T) {
	results := map[string]string{}
	var mu sync.Mutex
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		sub := subOf(req)
		if sub == "" {
			return reply(host(`[{"agent":"alpha","prompt":"run"},{"agent":"beta","prompt":"run"}]`)(req), onText)
		}
		if rs := lastUser(req).ToolResults; len(rs) > 0 {
			mu.Lock()
			results[sub] = rs[0].Content
			mu.Unlock()
			return reply(&llm.Response{Text: sub + " ran it"}, onText)
		}
		cmd := `echo hi`
		if sub == "beta" {
			cmd = `touch marker`
		}
		return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("b1", tools.Bash, `{"command":"`+cmd+`"}`)}}, nil
	}
	a, j, sh, ui, cwd := newSubAgent(t, prov, def("alpha"), def("beta", "Bash(echo *)"))
	if err := a.Start(context.Background(), "run", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(sh.handed) != 0 {
		t.Errorf("handed off to the live shell: %q", sh.handed)
	}
	if r := results["alpha"]; !strings.HasPrefix(r, "hi\n") || !strings.HasSuffix(r, "[exit 0, cwd "+cwd+"]") {
		t.Errorf("alpha's result %q", r)
	}
	if r := results["beta"]; !strings.Contains(r, "not run: touch is not among the commands") || !strings.Contains(r, "[exit 126") {
		t.Errorf("beta's result %q", r)
	}
	if _, err := os.Stat(filepath.Join(cwd, "marker")); err == nil {
		t.Error("a command outside the subagent's Bash(...) ran")
	}
	if !strings.Contains(ui.String(), "❯\x1b[0m \x1b[1mecho hi") || !strings.Contains(ui.String(), "\nhi\n") {
		t.Errorf("the command and its output not shown:\n%s", ui.String())
	}
	if r := j.es[2].Output; r != "## alpha (ok)\nalpha ran it\n\n## beta (ok)\nbeta ran it" {
		t.Errorf("result %q", r)
	}
}

// A mistake in any task fails the call before a subagent runs.
func TestTaskRejectsBadCalls(t *testing.T) {
	for tasks, want := range map[string]string{
		`[{"agent":"alpha","prompt":"x"},{"agent":"nope","prompt":"y"}]`:  `tasks[1]: unknown subagent "nope" (there are alpha, beta)`,
		`[{"agent":"alpha","prompt":"x"},{"agent":"beta","prompt":"  "}]`: "tasks[1]: empty prompt for beta",
		`[]`: "tasks: at least one task is needed",
	} {
		prov := &subProvider{}
		prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
			if subOf(req) != "" {
				t.Errorf("%s: a subagent ran", tasks)
			}
			return reply(host(tasks)(req), onText)
		}
		a, j, _, _, cwd := newSubAgent(t, prov, def("alpha"), def("beta"))
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		if r := j.es[2]; !r.IsError || !strings.Contains(r.Output, want) {
			t.Errorf("%s: result %q, error %v", tasks, r.Output, r.IsError)
		}
	}
}

// panesUI shows each subagent in a pane of its own.
type panesUI struct {
	*fakeUI
	mu     sync.Mutex
	panes  []*fakePane
	events []string // the panes opened, finished and closed, in order
}

type fakePane struct {
	u     *panesUI
	title string
	mu    sync.Mutex
	out   bytes.Buffer
	exit  *int
}

func (u *panesUI) Pane(title string) Live {
	u.mu.Lock()
	defer u.mu.Unlock()
	p := &fakePane{u: u, title: title}
	u.panes = append(u.panes, p)
	u.events = append(u.events, "pane "+title)
	return p
}

func (u *panesUI) ClosePanes() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.events = append(u.events, "close")
}

func (p *fakePane) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.Write(b)
}

func (p *fakePane) Finish(exit int) {
	p.mu.Lock()
	p.exit = &exit
	p.mu.Unlock()
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	p.u.events = append(p.u.events, "finish "+p.title)
}

// A UI with panes gets one per subagent, each finished with its status; a
// subagent that fails is its block of the result, not the call's error.
func TestTaskPanes(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		switch subOf(req) {
		case "":
			return reply(host(`[{"agent":"alpha","prompt":"x"},{"agent":"beta","prompt":"y"}]`)(req), onText)
		case "alpha":
			return nil, errors.New("boom")
		}
		return reply(&llm.Response{Text: "beta reply"}, onText)
	}
	a, j, _, ui, cwd := newSubAgent(t, prov, def("alpha"), def("beta"))
	pu := &panesUI{fakeUI: ui}
	a.UI = pu
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(pu.panes) != 2 || pu.panes[0].title != "alpha" || pu.panes[1].title != "beta" {
		t.Fatalf("panes %v", pu.panes)
	}
	for i, want := range []int{1, 0} {
		if p := pu.panes[i]; p.exit == nil || *p.exit != want {
			t.Errorf("%s finished with %v, want %d", p.title, p.exit, want)
		}
	}
	if !strings.Contains(pu.panes[0].out.String(), "boom") || !strings.Contains(pu.panes[1].out.String(), "beta reply") {
		t.Errorf("panes got %q and %q", pu.panes[0].out.String(), pu.panes[1].out.String())
	}
	if strings.Contains(ui.String(), "beta reply") {
		t.Errorf("a pane's output on the terminal:\n%s", ui.String())
	}
	if r := j.es[2]; r.IsError || r.Output != "## alpha (error)\nboom\n\n## beta (ok)\nbeta reply" {
		t.Errorf("result %q, error %v", r.Output, r.IsError)
	}
}

// The panes of a call go once all its subagents are done, not with the
// last one running: those past maxParallel start as the first ones end,
// and the panes would close and open again between them. An interrupted
// call ends them all the same.
func TestTaskClosesPanes(t *testing.T) {
	var tasks []string
	for i := range maxParallel + 1 {
		tasks = append(tasks, fmt.Sprintf(`{"agent":"alpha","prompt":"job %d"}`, i+1))
	}
	for _, stop := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		prov := &subProvider{}
		prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
			switch {
			case subOf(req) == "":
				return reply(host("["+strings.Join(tasks, ",")+"]")(req), onText)
			case stop:
				cancel()
				return nil, ctx.Err()
			}
			return reply(&llm.Response{Text: "ok"}, onText)
		}
		a, _, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
		pu := &panesUI{fakeUI: ui}
		a.UI = pu
		if err := a.Start(ctx, "go", tools.Exec{Dir: cwd}); (err != nil) != stop {
			t.Fatalf("interrupted %v: err %v", stop, err)
		}
		cancel()
		ev := strings.Join(pu.events, "\n")
		if len(pu.panes) == 0 || strings.Count(ev, "finish alpha") != len(pu.panes) || strings.Count(ev, "close") != 1 || !strings.HasSuffix(ev, "\nclose") {
			t.Errorf("interrupted %v: %q", stop, pu.events)
		}
		if !stop && len(pu.panes) != maxParallel+1 {
			t.Errorf("%d panes", len(pu.panes))
		}
	}
}

// Ctrl+C stops the subagents and leaves the call of task pending, as any
// interrupted call.
func TestTaskInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"x"}]`)(req), onText)
		}
		cancel()
		return nil, ctx.Err()
	}
	a, j, _, _, cwd := newSubAgent(t, prov, def("alpha"))
	if err := a.Start(ctx, "go", tools.Exec{Dir: cwd}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Errorf("journal %s", got)
	}
}

// With no subagents there is no task tool.
func TestNoSubagentsNoTool(t *testing.T) {
	a := &Agent{Tools: tools.Load("")}
	a.AddSubagents(nil)
	if _, ok := a.Tools.Get(subName); ok {
		t.Error("task without subagents")
	}
}
