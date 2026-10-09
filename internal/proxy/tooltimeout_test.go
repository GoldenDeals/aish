package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// The agent's command that the shell runs past its timeout is stopped the
// way Esc stops it, after the request that handed it off has returned:
// $AISH_RUN/esc names it with 124, the foreground gets SIGINT, the shell
// ends it with agent-end and that code, and the model gets its output with
// "[timed out after …]"; the request goes on.
func TestToolTimeoutCommand(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"sleep 30","timeout":0.3}`)}}},
		{Text: "went on"},
	}}
	p, out, cwd := hosted(t, prov)
	fg := newForeground(t, p)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "wait", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	fg.stand(t)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;sleep 30"})
	p.output([]byte("waiting\r\n"))
	if !fg.interrupted(5 * time.Second) {
		t.Fatal("the command got no SIGINT")
	}
	fg.back()
	if b, _ := os.ReadFile(filepath.Join(p.run, "esc")); string(b) != "c1 124\n" {
		t.Errorf("esc holds %q", b)
	}
	p.marker(Marker{Kind: "agent-end", Payload: "c1;124;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", RC: 124, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	want := "waiting\n[exit 124, cwd " + cwd + "]\n[timed out after 300ms]"
	if r := p.sess.Entries()[2].Output; r != want {
		t.Errorf("result %q, want %q", r, want)
	}
	if s := out.String(); !strings.Contains(s, "(timed out after 300ms)") {
		t.Errorf("the terminal does not tell why: %q", s)
	}
}

// The limit of a command the request left, Ctrl+C having cut it before it
// ran, stops nothing: the shell is not running it.
func TestToolTimeoutAfterRequest(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"sleep 30","timeout":0.2}`)}}},
	}}
	p, _, cwd := hosted(t, prov)
	newForeground(t, p)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "wait", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "130;" + cwd})
	time.Sleep(500 * time.Millisecond)
	p.mu.Lock()
	stop := p.stop
	p.mu.Unlock()
	if b, _ := os.ReadFile(filepath.Join(p.run, "esc")); len(b) != 0 || stop != nil {
		t.Errorf("stopped after the request: esc %q, stop %+v", b, stop)
	}
}
