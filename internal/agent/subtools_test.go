package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/skills"
	"github.com/inebotov/aish/internal/tools"
)

// subCommands answers a subagent's requests with a bash call of each of
// cmds in turn, then with a reply; the results it got are in results.
func subCommands(name string, cmds ...string) (*subProvider, *[]string) {
	var mu sync.Mutex
	var results []string
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"`+name+`","prompt":"look"}]`)(req), onText)
		}
		n := 0
		for _, m := range req.Messages {
			n += len(m.ToolResults)
		}
		if n > 0 {
			mu.Lock()
			results = append(results, lastUser(req).ToolResults[0].Content)
			mu.Unlock()
		}
		if n < len(cmds) {
			return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("b"+string(rune('0'+n)), tools.Bash, `{"command":"`+cmds[n]+`"}`)}}, nil
		}
		return reply(&llm.Response{Text: "looked"}, onText)
	}
	return prov, &results
}

// tools: Read, Grep is Claude Code's subagent that only reads: its bash
// searches, and a command that writes does not run.
func TestReadOnlySubagent(t *testing.T) {
	prov, results := subCommands("gamma", "rm x", "grep -r needle .")
	a, _, _, _, cwd := newSubAgent(t, prov, def("gamma", "Read", "Grep"))
	if err := os.WriteFile(filepath.Join(cwd, "x"), []byte("a needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(*results) != 2 {
		t.Fatalf("results %q", *results)
	}
	if r := (*results)[0]; !strings.Contains(r, "not run: rm is not among the commands") {
		t.Errorf("rm: %q", r)
	}
	if _, err := os.Stat(filepath.Join(cwd, "x")); err != nil {
		t.Errorf("rm ran: %v", err)
	}
	if r := (*results)[1]; !strings.Contains(r, "./x:a needle") || !strings.Contains(r, "[exit 0") {
		t.Errorf("grep: %q", r)
	}
}

// subModels are the configs the provider of subModel was made with.
var subModels struct {
	sync.Mutex
	cfgs []config.Config
}

const subModel = "subtools-test"

func init() {
	llm.Register(subModel, "", func(cfg config.Config) (llm.Provider, error) {
		subModels.Lock()
		subModels.cfgs = append(subModels.cfgs, cfg)
		subModels.Unlock()
		p := &subProvider{}
		p.answer = func(_ context.Context, _ llm.Request, onText func(string)) (*llm.Response, error) {
			return reply(&llm.Response{Text: "own model"}, onText)
		}
		return p, nil
	})
}

// A subagent with a model of its own runs it at the provider's default
// effort and with no window: the host's are its model's, which another
// model may not take (the provider here takes no effort at all).
func TestSubagentModelOwnEffort(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"x"},{"agent":"beta","prompt":"y"}]`)(req), onText)
		}
		return reply(&llm.Response{Text: "host model"}, onText)
	}
	alpha, beta := def("alpha"), def("beta")
	alpha.Model, beta.Model = "other", "host"
	a, j, _, _, cwd := newSubAgent(t, prov, alpha, beta)
	a.Cfg.Provider, a.Cfg.Model, a.Cfg.Effort, a.Cfg.ContextWindow = subModel, "host", "high", 1_000_000
	subModels.Lock()
	subModels.cfgs = nil
	subModels.Unlock()
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2].Output; r != "## alpha (ok)\nown model\n\n## beta (ok)\nhost model" {
		t.Errorf("result %q", r)
	}
	subModels.Lock()
	defer subModels.Unlock()
	if len(subModels.cfgs) != 1 {
		t.Fatalf("%d providers made, want 1 (alpha's)", len(subModels.cfgs))
	}
	if c := subModels.cfgs[0]; c.Model != "other" || c.Effort != "" || c.ContextWindow != 0 {
		t.Errorf("alpha's provider made with model %q, effort %q, window %d", c.Model, c.Effort, c.ContextWindow)
	}
}

func TestSubToolsEntries(t *testing.T) {
	host := tools.Load("")
	for _, tc := range []struct {
		names    []string
		tools    string
		patterns []string
		readOnly bool
		limited  bool
	}{
		{[]string{"Bash"}, "bash", nil, false, false},
		{[]string{"Bash()"}, "bash", nil, false, false},
		{[]string{"Bash(git log, git diff)"}, "bash", []string{"git log", "git diff"}, false, true},
		{[]string{"Grep", "Read"}, "bash read_file", nil, true, true},
		{[]string{"LS", "Bash"}, "bash", nil, false, false},
		{[]string{"glob", "Bash(git *)"}, "bash", []string{"git *"}, true, true},
		{[]string{"Grep(src/**)"}, "", nil, false, false},
	} {
		reg, scope := subTools(host, tc.names)
		var names []string
		for _, t := range reg.All() {
			names = append(names, t.Name())
		}
		if got := strings.Join(names, " "); got != tc.tools {
			t.Errorf("%q: tools %q, want %q", tc.names, got, tc.tools)
		}
		switch {
		case (scope != nil) != tc.limited:
			t.Errorf("%q: scope %v", tc.names, scope)
		case scope != nil && (!slices.Equal(scope.patterns, tc.patterns) || scope.readOnly != tc.readOnly):
			t.Errorf("%q: patterns %q, read-only %v", tc.names, scope.patterns, scope.readOnly)
		}
	}
	reg, scope := subTools(host, []string{"Grep"})
	if why := refused(scope, "rm x", "/"); !strings.Contains(why, "rm is not among the commands this subagent may run: cat *, find *") {
		t.Errorf("rm with Grep: %q", why)
	}
	if b, _ := reg.Get(tools.Bash); !strings.Contains(b.Desc(), "-delete") || !strings.Contains(b.Desc(), "grep *, head *") {
		t.Errorf("the read-only bash is described as %q", b.Desc())
	}
}

// The bash of Grep, Glob and LS runs the commands that search and read, so
// long as nothing in the line makes them write or run something else.
func TestReadOnlyBash(t *testing.T) {
	dir := t.TempDir()
	ro := &bashScope{readOnly: true}
	for _, cmd := range []string{
		"grep -r x .",
		"rg -n 'func main$' src",
		`find . -name '*.go' -type f`,
		`find . -name "*.go" | wc -l`,
		"find . -executable",
		"ls -la /tmp && cat a b",
		"head -5 a; tail -n 3 a",
		"grep x a 2>/dev/null",
		"grep x a 2>&1 | head",
		"LC_ALL=C grep x a",
		`for f in a b; do wc -l "$f"; done`,
		"cat $(find . -name x)",
		"f=a; cat $f",
	} {
		if why := refused(ro, cmd, dir); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	for cmd, want := range map[string]string{
		"rm x":                          "rm is not among",
		"/bin/rm x":                     "/bin/rm is not among",
		"find . -delete":                "find -delete changes files",
		`find . -exec rm {} \;`:         "find -exec ",
		"find . -execdir rm {} +":       "find -execdir",
		`find . -ok rm {} \;`:           "find -ok ",
		`find . -okdir rm {} \;`:        "find -okdir",
		"find . -fprint out":            "find -fprint ",
		"find . -fprintf out %p":        "find -fprintf",
		"find . -fls out":               "find -fls",
		"find . -de'lete'":              "find -delete",
		"rg --pre sh x":                 "rg --pre ",
		"rg --pre=sh x":                 "rg --pre=sh",
		"rg --hostname-bin=sh x":        "rg --hostname-bin=sh",
		"eval 'rm x'":                   "eval is not among",
		"bash -c 'rm x'":                "bash is not among",
		`eval "$x"`:                     "made at run time (computed)",
		"$x a":                          "made at run time",
		"grep x a > out":                "writes to " + filepath.Join(dir, "out"),
		"cat a >> out":                  "writes to",
		"> out":                         "writes to",
		"grep x a &> out":               "writes to",
		"cat a >| out":                  "writes to",
		"cat a 1<>out":                  "writes to",
		`grep x a > "$f"`:               "made at run time",
		"x=-delete; find . $x":          "find has a word made at run time, $x",
		"find . $(cat opts)":            "find has a word made at run time, $(cat opts)",
		"find * -name x":                "find has a word made at run time, *",
		"rg foo *.go":                   "rg has a word made at run time, *.go",
		"PATH=. cat a":                  "sets PATH",
		"LD_PRELOAD=./x.so cat a":       "sets LD_PRELOAD",
		"export PATH=.; cat a":          "export sets variables",
		"for PATH in .; do cat a; done": "sets PATH",
		"grep x a | tee out":            "tee is not among",
		"find . -name x | xargs rm":     "xargs is not among",
		"timeout 5 cat a":               "timeout is not among",
		"cat <(rm y)":                   "rm is not among",
		"cd /tmp && ls":                 "cd is not among",
	} {
		if why := refused(ro, cmd, dir); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q in it", cmd, why, want)
		}
	}
	// The commands of Bash(...) may write, but not the read-only ones
	// beside them.
	mixed := &bashScope{patterns: []string{"git log *"}, readOnly: true}
	for cmd, ok := range map[string]bool{
		"git log > out":        true,
		"git log | grep x":     true,
		"git log > out; cat a": false,
		"git log; cat a > out": false,
	} {
		if got := refused(mixed, cmd, dir) == ""; got != ok {
			t.Errorf("%q with git log *: allowed %v, want %v (%s)", cmd, got, ok, refused(mixed, cmd, dir))
		}
	}
	// A line of no command at all may not write either.
	if why := refused(&bashScope{patterns: []string{"git *"}}, "> out", dir); !strings.Contains(why, "writes to") {
		t.Errorf("> out with git *: %q", why)
	}
}

// serverTool is a tool of an MCP server, named as internal/mcp names it.
type serverTool struct{ name, server string }

func (t serverTool) Name() string         { return t.name }
func (serverTool) Desc() string           { return "" }
func (serverTool) Args() []tools.Arg      { return nil }
func (serverTool) Schema() map[string]any { return tools.Schema(nil) }
func (t serverTool) Server() string       { return t.server }
func (serverTool) Execute(context.Context, tools.Exec, map[string]any, io.Writer) (string, error) {
	return "", nil
}

// mcp__SERVER__TOOL and Skill of Claude Code give the MCP tools and the
// skills of the host.
func TestSubToolsMCPAndSkills(t *testing.T) {
	host := tools.Load("")
	for _, tl := range []tools.Tool{
		serverTool{"github_search_issues", "github"}, serverTool{"github_get_pr", "github"},
		serverTool{"my_srv_fetch", "my.srv"}, skills.Skill{Name: "deploy", Desc: "Deploys"}.Tool(),
	} {
		host.Add(tl)
	}
	for _, tc := range []struct {
		names []string
		tools string
	}{
		{[]string{"mcp__github__search_issues"}, "github_search_issues"},
		{[]string{"mcp__github"}, "github_search_issues github_get_pr"},
		{[]string{"mcp__github__*"}, "github_search_issues github_get_pr"},
		{[]string{"mcp__my_srv__fetch"}, "my_srv_fetch"},
		{[]string{"mcp__other__search_issues", "mcp__github__nope"}, ""},
		{[]string{"github_get_pr"}, "github_get_pr"},
		{[]string{"Skill", "Read"}, "read_file deploy"},
		{[]string{"Skill(other)"}, ""},
	} {
		reg, _ := subTools(host, tc.names)
		var names []string
		for _, t := range reg.All() {
			names = append(names, t.Name())
		}
		if got := strings.Join(names, " "); got != tc.tools {
			t.Errorf("%q: tools %q, want %q", tc.names, got, tc.tools)
		}
	}
}
