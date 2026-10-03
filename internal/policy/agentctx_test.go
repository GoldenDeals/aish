package policy

import "testing"

// The calls of a subagent carry its name as context.agent in every action,
// those of the host agent carry none: a rule may leave a subagent less than
// the host, and a policy that does not know of agent judges both alike.
func TestAgentContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := mustLoad(t, map[string]string{"a.cedar": permitAll + `
@reason("reviewer only reads")
forbid(principal, action == Action::"write", resource)
when { context has agent && context.agent == "reviewer" };
@reason("no git for reviewer")
forbid(principal, action == Action::"run", resource == Command::"git")
when { context has agent && context.agent == "reviewer" };
@reason("no tools for reviewer")
forbid(principal, action == Action::"call", resource)
when { context has agent && context.agent == "reviewer" };
`})
	write := NewInput("write_file", map[string]any{"path": "x", "content": "y"}, home, nil)
	git := callInput("bash", map[string]any{"command": "git status"}, home)
	redirect := callInput("bash", map[string]any{"command": "echo y > x"}, home)
	weather := NewInput("weather", map[string]any{"city": "Oslo"}, home, nil)
	for _, c := range []struct {
		name string
		in   Input
	}{{"write_file", write}, {"bash git", git}, {"bash redirect", redirect}, {"call", weather}} {
		for agent, want := range map[string]string{"": Allow, "reviewer": Deny, "tester": Allow} {
			in := c.in
			in.Agent = agent
			if d := check(t, e, in); d.Action != want {
				t.Errorf("%s by %q: %+v, want %s", c.name, agent, d, want)
			}
		}
	}

	// The engine of a subagent names it in whatever it checks, and the
	// host's engine stays as it was.
	sub := e.Subagent("reviewer")
	if d := check(t, sub, write); d.Action != Deny || d.Reason != "reviewer only reads" {
		t.Errorf("write_file by the engine of reviewer: %+v", d)
	}
	if d := check(t, e, write); d.Action != Allow {
		t.Errorf("write_file by the host's engine after Subagent: %+v", d)
	}
	if d := check(t, e.Subagent("tester"), write); d.Action != Allow {
		t.Errorf("write_file by the engine of tester: %+v", d)
	}
	var none *Engine
	if d := check(t, none.Subagent("reviewer"), write); d.Action != Allow {
		t.Errorf("write_file by no engine: %+v", d)
	}
}

// The example of README, «Политики», loads, and context.agent is known to
// has: a misspelled name next to it is still a load error.
func TestAgentContextExample(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := mustLoad(t, map[string]string{"a.cedar": permitAll + `
@reason("reviewer only reads")
forbid(principal, action == Action::"write", resource)
when { context has agent && context.agent == "reviewer" };

@reason("reviewer only reads")
forbid(principal, action == Action::"run", resource)
when { context has agent && context.agent == "reviewer" &&
       !["cat", "grep", "head", "ls", "rg"].contains(context.program) };
`})
	sub := e.Subagent("reviewer")
	for cmd, want := range map[string]string{
		"grep -rn x .":       Allow,
		"rm -rf build":       Deny,
		"cat a > b":          Deny,
		"ls | head -n1":      Allow,
		"grep x $(touch y)":  Deny,
		"bash -c 'rm -f z'":  Deny,
		"head -c 10 a.txt":   Allow,
		"ls; git commit -am": Deny,
	} {
		if d := check(t, sub, callInput("bash", map[string]any{"command": cmd}, home)); d.Action != want {
			t.Errorf("reviewer: %s: %+v, want %s", cmd, d, want)
		}
		if d := check(t, e, callInput("bash", map[string]any{"command": cmd}, home)); d.Action != Allow {
			t.Errorf("host: %s: %+v", cmd, d)
		}
	}
	if _, err := load(t, map[string]string{"typo.cedar": permitAll +
		`forbid(principal, action, resource) when { context has agnet };` + "\n"}); err == nil {
		t.Error("a has typo next to agent loaded")
	}
}
