package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/tools"
)

// A tool of another name than bash that hands its calls to the shell has
// the policy judge the command it hands, as bash has: a denied one never
// reaches the shell. Both rules see it: one the line of ssh, the other
// the command ssh runs on the box.
func TestPolicyJudgesHandedOffCommand(t *testing.T) {
	pol, err := policy.Load(context.Background(), "", policy.Rules{Deny: []string{"sudo *", "ssh box sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "remote", `{"what":"sudo ls"}`)}},
		{Text: "denied"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Policy = pol
	a.Tools.Add(remoteShell{probeTool{name: "remote"}})
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	const msg = `denied by policy: matches "ssh box sudo *"; matches "sudo *"`
	if len(j.es) < 3 {
		t.Fatalf("journal %s, handed off %q", kinds(j.es), sh.handed)
	}
	if r := j.es[2]; r.ToolCallID != "c1" || !r.IsError || r.Output != msg {
		t.Errorf("result %+v", r)
	}
	if len(sh.handed) != 0 {
		t.Errorf("handed off %q", sh.handed)
	}
	if !strings.Contains(ui.String(), "✗ "+msg) {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// A command a pre-tool hook puts in place of the model's is checked again
// as the command it is, of bash and of any tool handing its calls off.
func TestPolicyJudgesReplacedCommand(t *testing.T) {
	pol, err := policy.Load(context.Background(), "", policy.Rules{Deny: []string{"sudo *", "ssh box sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tool, args, reply, result string }{
		{"bash", `{"command":"ls"}`, `{"args":{"command":"sudo ls"}}`, `denied by policy: matches "sudo *"`},
		{"remote", `{"what":"ls"}`, `{"args":{"what":"sudo ls"}}`, `denied by policy: matches "ssh box sudo *"; matches "sudo *"`},
	} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", tc.tool, tc.args)}},
			{Text: "denied"},
		}}
		a, j, sh, _, cwd := newAgent(t, prov)
		a.Policy, a.Cfg.HooksDir = pol, ""
		a.Tools.Add(remoteShell{probeTool{name: "remote"}})
		hook(t, a, "pre-tool", "h", "cat >/dev/null; echo '"+tc.reply+"'")
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		if len(j.es) < 3 {
			t.Errorf("%s: journal %s, handed off %q", tc.tool, kinds(j.es), sh.handed)
			continue
		}
		if r := j.es[2]; r.ToolCallID != "c1" || !r.IsError || r.Output != tc.result {
			t.Errorf("%s: result %+v", tc.tool, r)
		}
		if len(sh.handed) != 0 {
			t.Errorf("%s: handed off %q", tc.tool, sh.handed)
		}
	}
}
