package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// lazyTool is a deferred tool of an MCP server. Its args make it a type ==
// panics on, as an MCP tool is.
type lazyTool struct {
	name, server, desc, inst string
	args                     []tools.Arg
}

func (t lazyTool) Name() string           { return t.name }
func (t lazyTool) Desc() string           { return t.desc }
func (t lazyTool) Args() []tools.Arg      { return t.args }
func (t lazyTool) Schema() map[string]any { return tools.Schema(t.args) }
func (t lazyTool) Server() string         { return t.server }
func (lazyTool) Hidden() bool             { return true }
func (t lazyTool) Instructions() string   { return t.inst }

func (t lazyTool) Execute(context.Context, tools.Exec, map[string]any, io.Writer) (string, error) {
	return t.name + " ran", nil
}

const gitlabNote = "Project paths are group/name."

// addLazy gives a the deferred tools of two servers, gitlab with
// instructions and exa without.
func addLazy(a *Agent) {
	for _, t := range []lazyTool{
		{name: "gitlab_get_issue", server: "gitlab", desc: "Get one issue. Returns its fields.", inst: gitlabNote},
		{name: "exa_web_search", server: "exa", desc: "Search the web for pages about an issue."},
		{name: "gitlab_listMergeRequests", server: "gitlab", desc: "List the merge requests of a project.", inst: gitlabNote},
		{name: "gitlab_create_note", server: "gitlab", desc: "Comment on an issue or a merge request.", inst: gitlabNote},
	} {
		a.Tools.Add(t)
	}
}

func search(t *testing.T, a *Agent, args map[string]any) (string, error) {
	t.Helper()
	st, ok := a.tool(toolSearch)
	if !ok {
		t.Fatal("no tool_search")
	}
	return st.Execute(context.Background(), tools.Exec{}, args, nil)
}

// The deferred tools reach the model by name in the system prompt, with
// the instructions of their servers, and tool_search instead of schemas.
func TestDeferredToolsInRequest(t *testing.T) {
	a, _, _, _, _ := newAgent(t, nil)
	addLazy(a)
	a.Tools.Add(lazyTool{name: "huge_x", server: "huge", inst: strings.Repeat("é", 3000)})
	req := a.request(nil)
	names := toolNames(req)
	if slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "gitlab") || strings.Contains(n, "exa") }) {
		t.Errorf("deferred tools sent whole: %v", names)
	}
	if names[len(names)-1] != toolSearch || !slices.Contains(names, tools.Bash) {
		t.Errorf("tools %v, want the built-in ones and tool_search last", names)
	}
	sys := req.System
	for _, want := range []string{
		"# MCP Server Instructions\nThe following MCP servers have provided instructions for how to use their tools:\n\n" +
			"## gitlab\n" + gitlabNote + "\n\n## huge\n",
		"# Deferred tools\n",
		"\ngitlab (3): gitlab_get_issue, gitlab_listMergeRequests, gitlab_create_note\nexa (1): exa_web_search\nhuge (1): huge_x",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt without %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "## exa") {
		t.Error("a server without instructions has a section")
	}
	if strings.Contains(sys, "# Additional tools") || strings.Contains(sys, "aish tool SERVER_TOOL") {
		t.Error("the old note on MCP tools is still there")
	}
	_, after, _ := strings.Cut(sys, "## huge\n")
	huge, _, _ := strings.Cut(after, "\n")
	if len(huge) > instructionsMax || !strings.HasSuffix(huge, "é…") {
		t.Errorf("long instructions: %d bytes, ending %q", len(huge), huge[max(0, len(huge)-8):])
	}
	// The same tools give the same prompt: it stays cached.
	if again := a.request(nil); again.System != sys || !slices.Equal(toolNames(again), names) {
		t.Error("the request changed with nothing else changed")
	}
}

// Without deferred tools there is no tool_search and nothing about them in
// the prompt.
func TestNoDeferredTools(t *testing.T) {
	a, _, _, _, _ := newAgent(t, nil)
	a.Tools.Add(probeTool{name: "shown", server: "srv"})
	req := a.request(nil)
	if slices.Contains(toolNames(req), toolSearch) {
		t.Error("tool_search without deferred tools")
	}
	if _, ok := a.tool(toolSearch); ok {
		t.Error("a call of tool_search finds a tool")
	}
	if strings.Contains(req.System, "# Deferred tools") || strings.Contains(req.System, "# MCP Server Instructions") {
		t.Errorf("sections without anything to say:\n%s", req.System)
	}
}

// A tool of the user's named tool_search keeps the name; the deferred
// tools go to the model whole then, as with expose: tools.
func TestOwnToolSearch(t *testing.T) {
	a, _, _, _, _ := newAgent(t, nil)
	own := probeTool{name: toolSearch}
	a.Tools.Add(own)
	addLazy(a)
	req := a.request(nil)
	names := toolNames(req)
	if !slices.Contains(names, "gitlab_get_issue") || !slices.Contains(names, "exa_web_search") {
		t.Errorf("deferred tools not sent whole: %v", names)
	}
	if n := strings.Count(strings.Join(names, " "), toolSearch); n != 1 {
		t.Errorf("tool_search %d times: %v", n, names)
	}
	if strings.Contains(req.System, "# Deferred tools") || !strings.Contains(req.System, "## gitlab\n"+gitlabNote) {
		t.Errorf("system prompt:\n%s", req.System)
	}
	if got, _ := a.tool(toolSearch); got.Desc() != own.Desc() {
		t.Error("the user's tool_search lost its name")
	}
}

func TestToolSearchSelect(t *testing.T) {
	a, _, _, _, _ := newAgent(t, nil)
	addLazy(a)
	out, err := search(t, a, map[string]any{"query": "select:gitlab_create_note, exa_web_search,nope,read_file,exa_web_search"})
	want := "loaded: gitlab_create_note, exa_web_search\n" +
		"gitlab_create_note: Comment on an issue or a merge request.\n" +
		"exa_web_search: Search the web for pages about an issue.\n" +
		"not found: nope\n" +
		"already available: read_file"
	if err != nil || out != want {
		t.Errorf("select: %v\n%s\nwant\n%s", err, out, want)
	}
	if out, err := search(t, a, map[string]any{"query": "select:nope"}); err == nil || out != "not found: nope" {
		t.Errorf("nothing selected: %q, %v", out, err)
	}
	// The exact name alone loads that tool, not its neighbours.
	if out, err := search(t, a, map[string]any{"query": "gitlab_get_issue"}); err != nil || !strings.HasPrefix(out, "loaded: gitlab_get_issue\n") {
		t.Errorf("by name: %q, %v", out, err)
	}
}

func TestToolSearchKeywords(t *testing.T) {
	a, _, _, _, _ := newAgent(t, nil)
	addLazy(a)
	loaded := func(args map[string]any) string {
		t.Helper()
		out, err := search(t, a, args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		first, _, _ := strings.Cut(out, "\n")
		return first
	}
	// issue: in the name of get_issue (2), in the descriptions of the
	// others (1 each), which keep the order of the registry.
	if got := loaded(map[string]any{"query": "Issue"}); got != "loaded: gitlab_get_issue, exa_web_search, gitlab_create_note" {
		t.Errorf("issue: %s", got)
	}
	// merge requests: both words in the name of listMergeRequests (4),
	// in the description of create_note (2).
	if got := loaded(map[string]any{"query": "merge-requests"}); got != "loaded: gitlab_listMergeRequests, gitlab_create_note" {
		t.Errorf("merge requests: %s", got)
	}
	// Plurals and the like: issues finds issue.
	if got := loaded(map[string]any{"query": "issues", "max_results": float64(1)}); got != "loaded: gitlab_get_issue" {
		t.Errorf("max_results 1: %s", got)
	}
	if out, err := search(t, a, map[string]any{"query": "weather"}); err == nil || !strings.Contains(err.Error(), "no deferred tool matches") {
		t.Errorf("no match: %q, %v", out, err)
	}
}

// The tools tool_search loads go to the end of the tools, in the order
// they were loaded; the start of the list stays the same, and a summary
// unloads them.
func TestLoadedToolsAppended(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("s1", toolSearch, `{"query":"select:gitlab_create_note,exa_web_search"}`)}},
		{ToolCalls: []llm.ToolCall{toolCall("s2", toolSearch, `{"query":"get issue","max_results":1}`)}},
		{ToolCalls: []llm.ToolCall{toolCall("c1", "gitlab_get_issue", `{}`)}},
		{Text: "done"},
	}}
	a, j, _, _, cwd := newAgent(t, prov)
	addLazy(a)
	if err := a.Start(context.Background(), "find it", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 4 {
		t.Fatalf("%d requests", len(prov.requests))
	}
	base := toolNames(prov.requests[0])
	for i, added := range [][]string{nil, {"gitlab_create_note", "exa_web_search"}, {"gitlab_create_note", "exa_web_search", "gitlab_get_issue"}} {
		if got := toolNames(prov.requests[i]); !slices.Equal(got, append(slices.Clone(base), added...)) {
			t.Errorf("request %d: tools %v, want %v then %v", i, got, base, added)
		}
		if prov.requests[i].System != prov.requests[0].System {
			t.Errorf("request %d: the system prompt changed", i)
		}
	}
	if r := j.es[len(j.es)-2]; r.ToolCallID != "c1" || r.IsError || r.Output != "gitlab_get_issue ran" {
		t.Errorf("the loaded tool was not called as a tool: %+v", r)
	}

	// A summary leaves nothing of it.
	es := append(j.Entries(), session.Entry{Kind: session.KindSummary, Text: "sum"})
	if got := toolNames(a.request(es)); !slices.Equal(got, base) {
		t.Errorf("after a summary: %v", got)
	}
}

// A deferred tool called without being loaded (a hook changed the result
// of tool_search, the model knew the name) runs, and the history with its
// call has its definition from then on.
func TestCalledToolDefined(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "exa_web_search", `{}`)}},
		{Text: "done"},
	}}
	a, j, _, _, cwd := newAgent(t, prov)
	addLazy(a)
	if err := a.Start(context.Background(), "search", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2]; r.IsError || r.Output != "exa_web_search ran" {
		t.Errorf("result %+v", r)
	}
	names := toolNames(prov.requests[1])
	if names[len(names)-1] != "exa_web_search" {
		t.Errorf("tools after the call: %v", names)
	}

	// A result of tool_search that is an error loads nothing, nor does a
	// tool that is gone from the registry.
	es := []session.Entry{
		{Kind: session.KindUser, Text: "x"},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{{ID: "s1", Name: toolSearch}, {ID: "g1", Name: "gone_tool"}}},
		{Kind: session.KindToolResult, ToolCallID: "s1", ToolName: toolSearch, Output: "loaded: gitlab_get_issue", IsError: true},
		{Kind: session.KindToolResult, ToolCallID: "g1", ToolName: "gone_tool", Output: "x"},
	}
	if got := a.loaded(es); len(got) != 0 {
		t.Errorf("loaded %v", got)
	}
}

// tool_search goes through the policy as any tool.
func TestToolSearchPolicy(t *testing.T) {
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n" +
		`@reason("no search") forbid(principal, action == Action::"call", resource == Tool::"tool_search");` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("s1", toolSearch, `{"query":"select:exa_web_search"}`)}},
		{Text: "done"},
	}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Policy = pol
	addLazy(a)
	if err := a.Start(context.Background(), "search", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2]; !r.IsError || r.Output != "denied by policy: no search" {
		t.Errorf("result %+v", r)
	}
	if names := toolNames(prov.requests[1]); slices.Contains(names, "exa_web_search") {
		t.Errorf("a denied search loaded %v", names)
	}
}

// A subagent defers its own tools: those its file gives it, and only those
// are found by its tool_search.
func TestSubagentDeferredTools(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"look"}]`)(req), onText)
		}
		if len(lastUser(req).ToolResults) == 0 {
			return reply(&llm.Response{ToolCalls: []llm.ToolCall{toolCall("s1", toolSearch, `{"query":"select:stub_echo,stub_search"}`)}}, onText)
		}
		return reply(&llm.Response{Text: "found"}, onText)
	}
	a, _, _, _, cwd := newSubAgent(t, prov, def("alpha", "mcp__stub__search"))
	for _, n := range []string{"stub_search", "stub_echo"} {
		a.Tools.Add(lazyTool{name: n, server: "stub", desc: "Does " + n + ".", inst: "Stub notes."})
	}
	a.Tools.Add(lazyTool{name: "other_x", server: "other", desc: "Other."})
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	var subs []llm.Request
	for _, req := range prov.all() {
		if subOf(req) == "alpha" {
			subs = append(subs, req)
		} else if !strings.Contains(req.System, "stub (2): stub_search, stub_echo\nother (1): other_x") {
			t.Errorf("host's prompt:\n%s", req.System)
		}
	}
	if len(subs) != 2 {
		t.Fatalf("%d requests of the subagent", len(subs))
	}
	sys := subs[0].System
	if !strings.Contains(sys, "stub (1): stub_search") || strings.Contains(sys, "stub_echo") || strings.Contains(sys, "other_x") {
		t.Errorf("subagent's prompt:\n%s", sys)
	}
	if !strings.Contains(sys, "## stub\nStub notes.") {
		t.Errorf("subagent's prompt without its server's instructions:\n%s", sys)
	}
	if names := toolNames(subs[0]); !slices.Contains(names, toolSearch) || slices.Contains(names, "stub_search") {
		t.Errorf("subagent's tools %v", names)
	}
	res := subs[1].Messages[len(subs[1].Messages)-1].ToolResults
	if len(res) != 1 || !strings.HasPrefix(res[0].Content, "loaded: stub_search\n") || !strings.Contains(res[0].Content, "not found: stub_echo") {
		t.Errorf("subagent's search: %+v", res)
	}
	if names := toolNames(subs[1]); names[len(names)-1] != "stub_search" {
		t.Errorf("subagent's tools after the search: %v", names)
	}
}
