package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// A git pull the agent runs may change the hooks and tools of a trusted
// project: the trust is gone with it, and the rest of the request goes
// without them, as the next one would.
func TestTrustLostMidRequest(t *testing.T) {
	bash := func(id, cmd string) *llm.Response {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		return &llm.Response{ToolCalls: []llm.ToolCall{{ID: id, Name: "bash", Args: args}}}
	}
	p, out, cwd := hosted(t, &scripted{replies: []*llm.Response{bash("c1", "git pull"), bash("c2", "make"), {Text: "done"}}})
	t.Setenv("XDG_DATA_HOME", filepath.Join(filepath.Dir(cwd), "data"))
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, s string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(cwd, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(config.ProjectFile, "hooks_dir = \".aish/hooks\"\ntools_dir = \".aish/tools\"\n", 0o644)
	write(".aish/hooks/pre-tool/mark", "#!/bin/sh\necho trusted >>ran\n", 0o755)
	write(".aish/tools/greet", "#!/bin/sh\n# aish:desc Greet.\necho hi\n", 0o755)
	if err := config.Trust(filepath.Join(cwd, config.ProjectFile)); err != nil {
		t.Fatal(err)
	}
	ran := func() string {
		b, _ := os.ReadFile(filepath.Join(cwd, "ran"))
		return string(b)
	}

	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "update", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if ran() != "trusted\n" {
		t.Fatalf("the hook of a trusted project ran: %q", ran())
	}
	if _, ok := p.ag.Tools.Get("greet"); !ok {
		t.Fatal("the tool of a trusted project is not there")
	}

	// What the pull brought.
	write(".aish/hooks/pre-tool/mark", "#!/bin/sh\necho pulled >>ran\n", 0o755)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;git pull"})
	p.output([]byte("Updating 1..2\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", RC: 0, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if cmd, _ := os.ReadFile(filepath.Join(p.run, "next.cmd")); string(cmd) != "make" {
		t.Fatalf("the next command was not handed off: %q", cmd)
	}
	if strings.Contains(ran(), "pulled") {
		t.Errorf("the changed hook ran: %q", ran())
	}
	if _, ok := p.ag.Tools.Get("greet"); ok {
		t.Error("the tool of a project no longer trusted is still there")
	}
	const line = "[aish: .aish.toml or its hooks/tools changed: project code is off until aish trust]"
	if n := strings.Count(out.String(), line); n != 1 {
		t.Errorf("told %d times: %q", n, out.String())
	}

	// The next step of the request is told no more.
	p.marker(Marker{Kind: "agent-start", Payload: "c2;make"})
	p.marker(Marker{Kind: "agent-end", Payload: "c2;0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c2", RC: 0, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), line); n != 1 {
		t.Errorf("told %d times after the next step", n)
	}
	if strings.Contains(ran(), "pulled") {
		t.Errorf("the changed hook ran on the next step: %q", ran())
	}
}
