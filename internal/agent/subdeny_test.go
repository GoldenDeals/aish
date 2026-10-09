package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/skills"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// denyHost is a host's registry with a tool of each kind: aish's, an MCP
// server's, a skill, the user's own (an external one: a plain tool here).
func denyHost() *tools.Registry {
	host := tools.Load("")
	for _, tl := range []tools.Tool{
		serverTool{"github_search_issues", "github"}, serverTool{"github_get_pr", "github"},
		serverTool{"jira_find", "jira"}, skills.Skill{Name: "deploy", Desc: "Deploys"}.Tool(),
		serverTool{"weather", ""},
	} {
		host.Add(tl)
	}
	return host
}

func regNames(reg *tools.Registry) string {
	var out []string
	for _, t := range reg.All() {
		out = append(out, t.Name())
	}
	return strings.Join(out, " ")
}

// limitedDef is subagent d of a file with tools, disallowedTools and
// permissionMode.
func limitedDef(tools, disallowed []string, mode string) subagent.Def {
	d := def("alpha", tools...)
	if tools == nil {
		d.Tools = nil
	}
	d.Disallowed, d.Mode = disallowed, mode
	return d
}

// scopeOf is the commands of a scope as the subagent's bash describes them;
// "*" for anything, "-" for no bash at all.
func scopeOf(reg *tools.Registry, s *bashScope) string {
	b, ok := reg.Get(tools.Bash)
	switch {
	case !ok:
		return "-"
	case b.(subBash).scope != s:
		return "the scope of the bash is not the one returned"
	case s == nil:
		return "*"
	}
	return s.String()
}

const readScope = "cat *, find *, grep *, head *, ls *, rg *, tail *, wc *"

func TestDisallowedTools(t *testing.T) {
	host := denyHost()
	all := "bash read_file write_file edit_file github_search_issues github_get_pr jira_find deploy weather"
	for _, tc := range []struct {
		tools, disallowed []string
		want, scope       string
	}{
		{nil, nil, all, "*"},
		{nil, []string{"Bash", "Write"}, "read_file edit_file github_search_issues github_get_pr jira_find deploy weather", "-"},
		{nil, []string{"bash", "WRITE_FILE"}, "read_file edit_file github_search_issues github_get_pr jira_find deploy weather", "-"},
		{[]string{"Bash"}, []string{"Bash(rm *)"}, "", "-"},
		{[]string{"Bash(git *)", "Read"}, []string{"Bash(git push *)"}, "read_file", "-"},
		{[]string{"Grep", "Read"}, []string{"Bash"}, "read_file", "-"},
		{[]string{"Grep", "Read"}, []string{"Glob"}, "read_file", "-"},
		{[]string{"LS", "Bash(git *)"}, []string{"Grep"}, "bash", "git *"},
		{[]string{"Grep", "Bash"}, []string{"LS"}, "bash", "*"},
		{nil, []string{"Grep"}, all, "*"},
		{[]string{"Grep", "Read"}, []string{"WebFetch", "Task", "Agent", "ask_user"}, "bash read_file", readScope},
		{nil, []string{"Read(src/**)", "MultiEdit"}, "bash write_file github_search_issues github_get_pr jira_find deploy weather", "*"},
		{nil, []string{"mcp__github"}, "bash read_file write_file edit_file jira_find deploy weather", "*"},
		{nil, []string{"mcp__github__get_pr", "mcp__other"}, "bash read_file write_file edit_file github_search_issues jira_find deploy weather", "*"},
		{nil, []string{"mcp__*"}, "bash read_file write_file edit_file deploy weather", "*"},
		{nil, []string{"Skill(other)", "WEATHER"}, "bash read_file write_file edit_file github_search_issues github_get_pr jira_find", "*"},
		{[]string{"Read", "Write"}, []string{"Write", "Read"}, "", "-"},
		{[]string{"Read"}, []string{"Write", "Bash"}, "read_file", "-"},
	} {
		reg, scope := defTools(host, limitedDef(tc.tools, tc.disallowed, ""))
		if got, s := regNames(reg), scopeOf(reg, scope); got != tc.want || s != tc.scope {
			t.Errorf("tools %q, disallowed %q: %q with bash %q, want %q with %q", tc.tools, tc.disallowed, got, s, tc.want, tc.scope)
		}
	}
}

func TestPlanMode(t *testing.T) {
	host := denyHost()
	for _, tc := range []struct {
		tools       []string
		want, scope string
	}{
		{nil, "bash read_file deploy", readScope},
		{[]string{"Bash", "Write", "Edit", "mcp__github", "weather"}, "bash", readScope},
		{[]string{"Grep", "Bash(git *)", "Read"}, "bash read_file", readScope},
		{[]string{"Bash(git log *)", "Read", "Skill"}, "read_file deploy", "-"},
		{[]string{"Write"}, "", "-"},
	} {
		reg, scope := defTools(host, limitedDef(tc.tools, nil, subagent.Plan))
		if got, s := regNames(reg), scopeOf(reg, scope); got != tc.want || s != tc.scope {
			t.Errorf("tools %q: %q with bash %q, want %q with %q", tc.tools, got, s, tc.want, tc.scope)
		}
	}
	// With what disallowedTools takes on top.
	reg, scope := defTools(host, limitedDef(nil, []string{"Read", "Skill"}, subagent.Plan))
	if got := regNames(reg); got != "bash" || scope == nil || !scope.readOnly {
		t.Errorf("plan less Read and Skill: %q, scope %v", got, scope)
	}
	reg, _ = defTools(host, limitedDef(nil, []string{"Bash"}, subagent.Plan))
	if got := regNames(reg); got != "read_file deploy" {
		t.Errorf("plan less Bash: %q", got)
	}

	_, scope = defTools(host, limitedDef(nil, nil, subagent.Plan))
	dir := t.TempDir()
	for cmd, want := range map[string]string{
		"echo x > f":          "echo is not among the commands",
		"cat a > f":           "writes to",
		"rm -rf x":            "rm is not among the commands",
		"find . -delete":      "find -delete changes files",
		"git commit -m x":     "git is not among the commands",
		"grep -r x . | wc -l": "",
	} {
		why := refused(scope, cmd, dir, nil)
		if want == "" && why != "" || !strings.Contains(why, want) {
			t.Errorf("%q in plan: %q, want %q", cmd, why, want)
		}
	}
}

// The modes that would lift checks, and those that leave them as they are,
// give the subagent what it has without a mode.
func TestModesWithoutEffect(t *testing.T) {
	host := denyHost()
	for _, names := range [][]string{nil, {"Bash"}, {"Grep", "Read"}, {"Bash(git *)", "Write"}, {"mcp__github", "Skill"}} {
		want, wantScope := defTools(host, limitedDef(names, nil, ""))
		for _, mode := range []string{"default", "manual", "dontAsk", "acceptEdits", "auto", "bypassPermissions"} {
			reg, scope := defTools(host, limitedDef(names, nil, mode))
			if got := scopeOf(reg, scope); got != scopeOf(want, wantScope) {
				t.Errorf("%q %s: bash %q, want %q", names, mode, got, scopeOf(want, wantScope))
			}
			if a, b := regNames(reg), regNames(want); a != b {
				t.Errorf("%q %s: %q, want %q", names, mode, a, b)
			}
		}
	}
}

// No value of disallowedTools or permissionMode gives a subagent a tool, or
// a command of its bash, that its tools field alone does not.
func TestLimitsNeverWiden(t *testing.T) {
	host := denyHost()
	entries := []string{"Bash", "Bash(git *)", "Bash(cat *)", "Bash(*)", "Grep", "Glob", "LS", "Read", "Write", "Edit",
		"MultiEdit", "Skill", "Skill(x)", "mcp__github", "mcp__*", "mcp__jira__find", "weather", "Read(src/**)", "Task", "nope"}
	var lists [][]string
	lists = append(lists, nil, []string{})
	for i, a := range entries {
		lists = append(lists, []string{a})
		for _, b := range entries[i+1:] {
			lists = append(lists, []string{a, b})
		}
	}
	modes := []string{"", "default", "manual", "dontAsk", "acceptEdits", "auto", "bypassPermissions", subagent.Plan}
	for _, tl := range lists {
		base, baseScope := subTools(host, tl)
		for _, dl := range lists {
			for _, mode := range modes {
				reg, scope := defTools(host, limitedDef(tl, dl, mode))
				for _, tool := range reg.All() {
					if _, ok := base.Get(tool.Name()); !ok {
						t.Fatalf("tools %q, disallowed %q, %q: gave %s", tl, dl, mode, tool.Name())
					}
				}
				if _, ok := reg.Get(tools.Bash); ok && !narrower(scope, baseScope) {
					t.Fatalf("tools %q, disallowed %q, %q: bash %v wider than %v", tl, dl, mode, scope, baseScope)
				}
			}
		}
	}
}

// narrower tells whether every command scope s lets run, base lets run too.
func narrower(s, base *bashScope) bool {
	switch {
	case base == nil:
		return true
	case s == nil:
		return false
	}
	return (!s.readOnly || base.readOnly) && !slices.ContainsFunc(s.patterns, func(p string) bool {
		return !slices.Contains(base.patterns, p)
	})
}

// A subagent in plan mode is offered no tool that writes, and its bash
// does not write; under aish yolo its bash goes past the read-only scope,
// as a Grep subagent's does, but the tools plan and disallowedTools took
// are not offered again, and a call of one does not run.
func TestPlanSubagentUnderYolo(t *testing.T) {
	for _, on := range []bool{false, true} {
		var mu sync.Mutex
		var offered [][]string
		var results []string
		prov := &subProvider{}
		prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
			if subOf(req) == "" {
				return reply(host(`[{"agent":"alpha","prompt":"write"}]`)(req), onText)
			}
			mu.Lock()
			defer mu.Unlock()
			offered = append(offered, toolNames(req))
			n := 0
			for _, m := range req.Messages {
				n += len(m.ToolResults)
			}
			if n > 0 {
				results = append(results, lastUser(req).ToolResults[0].Content)
			}
			switch n {
			case 0:
				return callOf("b0", tools.Bash, `{"command":"echo x > f"}`), nil
			case 1:
				b, _ := json.Marshal(map[string]string{"path": "w", "content": "x"})
				return callOf("w1", "write_file", string(b)), nil
			}
			return reply(&llm.Response{Text: "done"}, onText)
		}
		alpha := def("alpha")
		alpha.Mode = subagent.Plan
		a, _, _, _, cwd := newSubAgent(t, prov, alpha)
		a.Yolo = func() bool { return on }
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		for _, o := range offered {
			if slices.Contains(o, "write_file") || slices.Contains(o, "edit_file") {
				t.Errorf("yolo %v: plan offered %q", on, o)
			}
		}
		if len(results) != 2 {
			t.Fatalf("yolo %v: results %q", on, results)
		}
		if r := results[1]; !strings.Contains(r, "unknown tool write_file") {
			t.Errorf("yolo %v: write_file call: %q", on, r)
		}
		mu.Unlock()
		if _, err := os.Stat(filepath.Join(cwd, "w")); err == nil {
			t.Errorf("yolo %v: write_file wrote", on)
		}
		_, err := os.Stat(filepath.Join(cwd, "f"))
		if written := err == nil; written != on {
			t.Errorf("yolo %v: echo x > f ran: %v (%q)", on, written, results[0])
		}
	}
}

// disallowedTools under yolo: a subagent without bash gets none back.
func TestDisallowedUnderYolo(t *testing.T) {
	var mu sync.Mutex
	var offered [][]string
	var results []string
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"beta","prompt":"write"}]`)(req), onText)
		}
		mu.Lock()
		defer mu.Unlock()
		offered = append(offered, toolNames(req))
		if rs := lastUser(req).ToolResults; len(rs) > 0 {
			results = append(results, rs[0].Content)
			return reply(&llm.Response{Text: "done"}, onText)
		}
		return callOf("b0", tools.Bash, `{"command":"touch f"}`), nil
	}
	beta := def("beta")
	beta.Disallowed = []string{"Bash", "Write"}
	a, _, sh, _, cwd := newSubAgent(t, prov, beta)
	a.Yolo = func() bool { return true }
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, o := range offered {
		if slices.Contains(o, tools.Bash) || slices.Contains(o, "write_file") {
			t.Errorf("offered %q", o)
		}
	}
	if len(results) != 1 || strings.Contains(results[0], "[exit 0") {
		t.Errorf("results %q", results)
	}
	if _, err := os.Stat(filepath.Join(cwd, "f")); err == nil {
		t.Error("touch f ran")
	}
	if len(sh.handed) > 0 {
		t.Errorf("the live shell got %q", sh.handed)
	}
}
