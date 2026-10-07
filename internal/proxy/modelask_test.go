package proxy

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// A command of the agent runs in the user's shell, where `aish model` is
// at hand: text the agent read must not take the rest of the request to
// another model, or another profile's endpoint and key.
func TestModelRefusedToAgent(t *testing.T) {
	p, _ := profiled(t)
	p.effort = "high"
	other := rpc.ModelParams{Profile: "work", Model: "claude-sonnet-5"}

	p.marker(Marker{Kind: "ask-start"})
	for _, mp := range []rpc.ModelParams{other, {Profile: "local", Model: "qwen3:8b"}} {
		if _, err := call(t, p, rpc.MethodModel, mp); err == nil || !strings.Contains(err.Error(), "not by the assistant") {
			t.Errorf("%+v during a request: %v", mp, err)
		}
	}
	if p.profile != "work" || p.model != "claude-opus-5" || p.effort != "high" {
		t.Errorf("switched by the assistant: %q %q %q", p.profile, p.model, p.effort)
	}

	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	v, err := call(t, p, rpc.MethodModel, other)
	if err != nil {
		t.Fatalf("by the user: %v", err)
	}
	if i := v.(rpc.Info); i.Model != "claude-sonnet-5" || p.model != "claude-sonnet-5" || p.effort != "" {
		t.Errorf("by the user: %+v; now %q %q", i, p.model, p.effort)
	}
}
