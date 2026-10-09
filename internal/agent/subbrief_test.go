package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/tools"
)

const (
	briefSecret = "TOPSECRET-42"
	briefSkill  = "DEPLOY-INSTRUCTIONS"
	brief       = "summarize @secret.txt then /deploy prod"
)

// noReading is a policy that forbids reading files, and a cwd with a file
// to read and a skill kept from the model.
func noReading(t *testing.T, cwd string) *policy.Engine {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, "secret.txt"), []byte(briefSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, cwd, "deploy", "disable-model-invocation: true\n", briefSkill+" run ./deploy.sh $ARGUMENTS\n")
	dir := t.TempDir()
	src := "permit(principal, action, resource);\nforbid(principal, action == Action::\"read\", resource);\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

// sentText is all a request gives the model.
func sentText(req llm.Request) string {
	var b strings.Builder
	b.WriteString(req.System)
	for _, d := range req.Tools {
		b.WriteString("\n" + d.Description)
	}
	for _, m := range req.Messages {
		b.WriteString("\n" + m.Text)
		for _, r := range m.ToolResults {
			b.WriteString("\n" + r.Content)
		}
	}
	return b.String()
}

// The host's model, denied a file, may not get it through a subagent by
// naming it with @ in the brief, nor load a skill kept from it with /: the
// brief is not the user's request. The subagent reads with read_file,
// under the policy.
func TestSubagentBriefNotUserRequest(t *testing.T) {
	var mu sync.Mutex
	results := map[string]string{}
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		sub, rs := subOf(req), lastUser(req).ToolResults
		if len(rs) > 0 {
			mu.Lock()
			results[sub+"/"+rs[0].CallID] = rs[0].Content
			mu.Unlock()
		}
		switch {
		case sub == "" && len(rs) == 0:
			return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("r1", "read_file", `{"path":"secret.txt"}`)}}, nil
		case sub == "" && rs[0].CallID == "r1":
			return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("t1", subName, `{"tasks":[{"agent":"alpha","prompt":"`+brief+`"}]}`)}}, nil
		case sub == "alpha" && len(rs) == 0:
			return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("s1", "read_file", `{"path":"secret.txt"}`)}}, nil
		}
		return reply(&llm.Response{Text: "done"}, onText)
	}
	a, j, _, ui, cwd := newSubAgent(t, prov, def("alpha"))
	a.Policy = noReading(t, cwd)
	if err := a.Start(context.Background(), "have alpha sum up the secret", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"/r1", "alpha/s1"} {
		if r := results[id]; !strings.Contains(r, "denied by policy") {
			t.Errorf("read_file %s: %q", id, r)
		}
	}
	if r := results["/t1"]; !strings.Contains(r, "## alpha (ok)") {
		t.Fatalf("task result %q", r)
	}
	if got := kinds(j.es); strings.Contains(got, "file") || strings.Contains(got, "skill") {
		t.Errorf("host journal %s", got)
	}
	var briefs int
	for _, req := range prov.all() {
		sent, first := sentText(req), req.Messages[0].Text
		for _, s := range []string{briefSecret, briefSkill} {
			if strings.Contains(sent, s) {
				t.Errorf("%q's request has %s:\n%s", subOf(req), s, first)
			}
		}
		if subOf(req) != "alpha" {
			continue
		}
		briefs++
		if strings.Contains(sent, "The user mentioned") || strings.Contains(sent, "The user invoked") {
			t.Errorf("the brief passes for the user's request:\n%s", first)
		}
		if !strings.HasSuffix(first, brief) {
			t.Errorf("alpha's request %q", first)
		}
	}
	if briefs == 0 {
		t.Fatal("alpha never ran")
	}
	if out := ui.String(); strings.Contains(out, "@secret.txt (") || strings.Contains(out, "/deploy (") {
		t.Errorf("mention notes on the screen:\n%s", out)
	}
}

// The user's own request still brings the file and the skill: the user
// typed them, the policy is for the model's calls.
func TestUserRequestMentionsUnderReadPolicy(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "done"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Policy = noReading(t, cwd)
	if err := a.Start(context.Background(), brief, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "file skill user assistant" {
		t.Fatalf("journal %s", got)
	}
	sent := prov.requests[0].Messages[0].Text
	if !strings.Contains(sent, briefSecret) || !strings.Contains(sent, briefSkill+" run ./deploy.sh summarize @secret.txt then prod") {
		t.Errorf("sent:\n%s", sent)
	}
	if out := ui.String(); !strings.Contains(out, "@secret.txt (") || !strings.Contains(out, "/deploy (") {
		t.Errorf("terminal:\n%s", out)
	}
}

// The host's model learns from task's description that a brief brings no
// files: it names paths for the subagent to read. The description is part
// of the cached prefix, so it is the same on every turn.
func TestTaskDescPathsNotMentions(t *testing.T) {
	a, _, _, _, _ := newSubAgent(t, &subProvider{}, def("alpha"))
	tool, ok := a.Tools.Get(subName)
	if !ok {
		t.Fatal("no task tool")
	}
	desc := tool.Desc()
	const want = "Write file paths as they are: the subagent reads the files itself. " +
		"@file and /skill in a prompt attach nothing."
	if !strings.Contains(desc, want) {
		t.Errorf("task's description lacks %q:\n%s", want, desc)
	}
	if again := tool.Desc(); again != desc {
		t.Errorf("task's description changed between calls:\n%s\n---\n%s", desc, again)
	}
}
