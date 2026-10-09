package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
)

func bashCall(id, cmd string) *llm.Response {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return &llm.Response{ToolCalls: []llm.ToolCall{{ID: id, Name: "bash", Args: args}}}
}

// `aish compact` or `aish agent start` run by the assistant's own command
// would close the call the shell is running, and the agent_resume after it
// would find no call to go on with. They are refused while the command is
// the shell's; agent_resume is not, and back at the prompt compact works.
func TestNestedRequestRefused(t *testing.T) {
	p, _, cwd := hosted(t, &scripted{replies: []*llm.Response{
		bashCall("c1", "aish compact"),
		bashCall("c2", "sleep 9"),
		{Text: "the summary"},
	}})
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "tidy up", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	refused := func(when string) {
		t.Helper()
		for _, m := range []string{rpc.MethodCompact, rpc.MethodAgentStart} {
			_, err := call(t, p, m, rpc.AgentParams{Text: "nested", Cwd: cwd})
			if err == nil || !strings.Contains(err.Error(), "the assistant's command cannot start, compact or recap a request") {
				t.Errorf("%s %s: %v", m, when, err)
			}
		}
		if got := journalKinds(p.sess); got != "user assistant" {
			t.Fatalf("%s: journal %s", when, got)
		}
	}
	// The shell may run the command before the proxy reads its marker.
	refused("before agent-start")
	p.marker(Marker{Kind: "agent-start", Payload: "c1;aish compact"})
	refused("after agent-start")

	p.output([]byte("aish: refused\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;1;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", RC: 1, Cwd: cwd}); err != nil {
		t.Fatalf("agent_resume after the refused command: %v", err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}

	// Ctrl+C stops the next command: no agent-end, the prompt is back.
	p.marker(Marker{Kind: "agent-start", Payload: "c2;sleep 9"})
	p.marker(Marker{Kind: "cmd-end", Payload: "130;" + cwd})
	if _, err := call(t, p, rpc.MethodCompact, rpc.AgentParams{Cwd: cwd}); err != nil {
		t.Fatalf("compact at the prompt: %v", err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant tool_result summary" {
		t.Fatalf("journal %s", got)
	}
}

// Requests one after another in one command line (`ask` in a loop) are
// not nested: the first one is over once its command's output is taken.
func TestRequestsInOneCommandLine(t *testing.T) {
	p, _, cwd := hosted(t, &scripted{replies: []*llm.Response{
		bashCall("c1", "ls"),
		{Text: "two files"},
		{Text: "again"},
	}})
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "list", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c1;ls"})
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", RC: 0, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "list again", Cwd: cwd}); err != nil {
		t.Fatalf("the second request: %v", err)
	}
}
