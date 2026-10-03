package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/tools"
)

// The policy sees which subagent makes a call: a rule for alpha stops
// alpha's write and neither beta's nor the host's.
func TestPolicySeesSubagent(t *testing.T) {
	results := map[string]string{}
	var mu sync.Mutex
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		sub := subOf(req)
		rs := lastUser(req).ToolResults
		switch {
		case sub == "" && len(rs) == 0:
			return reply(host(`[{"agent":"alpha","prompt":"write"},{"agent":"beta","prompt":"write"}]`)(req), onText)
		case sub == "" && rs[0].CallID == "t1":
			return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("w1", "write_file", `{"path":"host.txt","content":"x"}`)}}, nil
		case len(rs) > 0:
			mu.Lock()
			results[sub] = rs[0].Content
			mu.Unlock()
			return reply(&llm.Response{Text: "done"}, onText)
		}
		return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("w1", "write_file", `{"path":"`+sub+`.txt","content":"x"}`)}}, nil
	}
	a, _, _, _, cwd := newSubAgent(t, prov, def("alpha"), def("beta"))
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n" + `@reason("alpha only reads")
forbid(principal, action == Action::"write", resource)
when { context has agent && context.agent == "alpha" };
`
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	a.Policy = pol
	if err := a.Start(context.Background(), "write", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := results["alpha"]; !strings.Contains(r, "denied by policy: alpha only reads") {
		t.Errorf("alpha's result %q", r)
	}
	if r := results["beta"]; strings.Contains(r, "denied") {
		t.Errorf("beta's result %q", r)
	}
	if r := results[""]; strings.Contains(r, "denied") {
		t.Errorf("the host's result %q", r)
	}
	for name, want := range map[string]bool{"alpha.txt": false, "beta.txt": true, "host.txt": true} {
		if _, err := os.Stat(filepath.Join(cwd, name)); (err == nil) != want {
			t.Errorf("%s written: %v, want %v", name, err == nil, want)
		}
	}
}
