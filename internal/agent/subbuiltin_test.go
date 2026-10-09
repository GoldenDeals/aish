package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// Explore, a subagent of aish's own, only reads: it has read_file and a
// bash that searches, not write_file or edit_file, and a line of its bash
// that writes does not run. general-purpose has all the host's tools but
// those of the host alone.
func TestExploreOnlyReads(t *testing.T) {
	calls := []llm.ToolCall{
		toolCall("w", "write_file", `{"path":"g","content":"x"}`),
		toolCall("b0", tools.Bash, `{"command":"echo x > f"}`),
		toolCall("b1", tools.Bash, `{"command":"cat x > f"}`),
		toolCall("b2", tools.Bash, `{"command":"grep -r needle ."}`),
	}
	var mu sync.Mutex
	var results []string
	var given []string // the tools of the subagent's requests
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if !strings.Contains(req.System, subNote) {
			return reply(host(`[{"agent":"Explore","prompt":"look"}]`)(req), onText)
		}
		mu.Lock()
		defer mu.Unlock()
		given = given[:0]
		for _, d := range req.Tools {
			given = append(given, d.Name)
		}
		n := 0
		for _, m := range req.Messages {
			n += len(m.ToolResults)
		}
		if n > 0 {
			results = append(results, lastUser(req).ToolResults[0].Content)
		}
		if n < len(calls) {
			return &llm.Response{ToolCalls: []llm.ToolCall{calls[n]}}, nil
		}
		return reply(&llm.Response{Text: "looked"}, onText)
	}
	a, _, _, _, cwd := newAgent(t, nil)
	a.Provider = prov
	defs, problems := subagent.Find(cwd)
	if len(defs) != 2 || len(problems) != 0 {
		t.Fatalf("defs %+v, problems %+v", defs, problems)
	}
	a.AddSubagents(defs)
	if _, ok := a.Tools.Get(subName); !ok {
		t.Fatalf("no %s without files", subName)
	}
	if err := os.WriteFile(filepath.Join(cwd, "x"), []byte("a needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(given)
	if !slices.Equal(given, []string{tools.Bash, "read_file"}) {
		t.Errorf("Explore's tools: %q", given)
	}
	if len(results) != 4 {
		t.Fatalf("results %q", results)
	}
	if r := results[0]; !strings.Contains(r, "unknown tool write_file") {
		t.Errorf("write_file: %q", r)
	}
	if r := results[1]; !strings.Contains(r, "not run: echo is not among the commands") {
		t.Errorf("echo x > f: %q", r)
	}
	if r := results[2]; !strings.Contains(r, "not run: writes to") {
		t.Errorf("cat x > f: %q", r)
	}
	for _, f := range []string{"f", "g"} {
		if _, err := os.Stat(filepath.Join(cwd, f)); err == nil {
			t.Errorf("%s written", f)
		}
	}
	if r := results[3]; !strings.Contains(r, "./x:a needle") {
		t.Errorf("grep: %q", r)
	}

	var gp subagent.Def
	for _, d := range defs {
		if d.Name == "general-purpose" {
			gp = d
		}
	}
	reg, scope := defTools(a.Tools, gp)
	var names []string
	for _, tl := range reg.All() {
		names = append(names, tl.Name())
	}
	for _, want := range []string{tools.Bash, "read_file", "write_file", "edit_file"} {
		if !slices.Contains(names, want) {
			t.Errorf("general-purpose has no %s: %q", want, names)
		}
	}
	if scope != nil || slices.ContainsFunc(names, func(n string) bool { return n == subName || strings.HasPrefix(n, "task_") }) {
		t.Errorf("general-purpose: %q, scope %v", names, scope)
	}
}
