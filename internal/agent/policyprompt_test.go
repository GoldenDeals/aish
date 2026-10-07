package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/tools"
)

const policySection = "# Policy\nThe user's policy limits what you may do. " +
	"Follow these rules from the start instead of finding them out by being refused:\n" +
	"- sudo is forbidden: use doas\n" +
	"- never push"

// hintedPolicy is an engine with a hint of [policy] and one of Cedar.
func hintedPolicy(t *testing.T) *policy.Engine {
	t.Helper()
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n" +
		`@hint("never push") forbid(principal, action == Action::"run", resource == Command::"git") when { context.args.contains("push") };` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{
		Deny:  []string{"sudo *"},
		Hints: map[string]string{"sudo *": "sudo is forbidden: use doas"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

// The hints go to the system prompt after the sections of the tools and
// before the user's own system_prompt, and stay the same from turn to
// turn.
func TestPolicyPrompt(t *testing.T) {
	a, _, _, _, _ := newAgent(t, nil)
	addLazy(a)
	a.Cfg.SystemPrompt = "USER-PROMPT"
	a.Policy = hintedPolicy(t)
	sys := a.request(nil).System
	deferred, pol, user := strings.Index(sys, "# Deferred tools"), strings.Index(sys, policySection), strings.Index(sys, "USER-PROMPT")
	if deferred < 0 || pol < 0 || user < 0 || !(deferred < pol && pol < user) {
		t.Errorf("tools at %d, policy at %d, system_prompt at %d:\n%s", deferred, pol, user, sys)
	}
	if !strings.Contains(sys, policySection+"\n\nUSER-PROMPT") {
		t.Errorf("the policy section is not right before system_prompt:\n%s", sys)
	}
	if again := a.request(nil).System; again != sys {
		t.Error("the system prompt changed with nothing else changed")
	}
	// Nothing else to say: the section alone.
	b, _, _, _, _ := newAgent(t, nil)
	b.Cfg.SystemPrompt = ""
	b.Policy = a.Policy
	if sys := b.request(nil).System; !strings.HasSuffix(sys, "\n\n"+policySection) {
		t.Errorf("without tools and system_prompt:\n%s", sys)
	}
}

// Without hints the system prompt is what it was before there were any.
func TestNoPolicyPrompt(t *testing.T) {
	a, _, _, _, _ := newAgent(t, nil)
	addLazy(a)
	a.Cfg.SystemPrompt = "USER-PROMPT"
	a.Policy = nil
	sys := a.request(nil).System
	if want := system(a.env, strings.TrimSpace(a.toolsPrompt()+"\n\nUSER-PROMPT")); sys != want {
		t.Errorf("without a policy:\n%s\nwant\n%s", sys, want)
	}
	plain, err := policy.Load(context.Background(), t.TempDir(), policy.Rules{Deny: []string{"sudo *"}, WriteOutsideHome: policy.Deny})
	if err != nil {
		t.Fatal(err)
	}
	for _, pol := range []*policy.Engine{{}, plain} {
		a.Policy = pol
		if got := a.request(nil).System; got != sys {
			t.Errorf("a policy without hints changed the system prompt:\n%s", got)
		}
	}
	if strings.Contains(sys, "# Policy") {
		t.Errorf("a policy section without hints:\n%s", sys)
	}
}

// A subagent's requests carry the hints of the host's policy: its engine
// is the host's.
func TestSubagentPolicyPrompt(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"look"}]`)(req), onText)
		}
		return reply(&llm.Response{Text: "seen"}, onText)
	}
	a, _, _, _, cwd := newSubAgent(t, prov, def("alpha"))
	a.Policy = hintedPolicy(t)
	if err := a.Start(context.Background(), "look", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	var sub, hosts int
	for _, req := range prov.all() {
		if !strings.Contains(req.System, policySection) {
			t.Errorf("a request of %q without the hints:\n%s", subOf(req), req.System)
		}
		if subOf(req) == "alpha" {
			sub++
		} else {
			hosts++
		}
	}
	if sub == 0 || hosts == 0 {
		t.Errorf("%d requests of the subagent, %d of the host", sub, hosts)
	}
}
