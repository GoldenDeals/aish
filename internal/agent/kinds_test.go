package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/tools"
)

// probeTool is of a kind the agent has no case for: it learns what the tool
// can do from the interfaces the tool implements.
type probeTool struct {
	name, server    string
	hidden, streams bool
}

var probeArgs = []tools.Arg{{Name: "what", Type: "string", Required: true}}

func (t probeTool) Name() string           { return t.name }
func (probeTool) Desc() string             { return "A probe" }
func (probeTool) Args() []tools.Arg        { return probeArgs }
func (t probeTool) Schema() map[string]any { return tools.Schema(probeArgs) }
func (t probeTool) Server() string         { return t.server }
func (t probeTool) Hidden() bool           { return t.hidden }
func (t probeTool) Streaming() bool        { return t.streams }

func (t probeTool) Execute(_ context.Context, ex tools.Exec, args map[string]any, live io.Writer) (string, error) {
	out := fmt.Sprintf("%s did %v in %s\n", t.name, args["what"], ex.Dir)
	if live != nil {
		io.WriteString(live, out)
	}
	return out, nil
}

// remoteShell hands its calls to the shell, as commands of its own.
type remoteShell struct{ probeTool }

func (remoteShell) Command(args map[string]any) (string, bool) {
	what, _ := args["what"].(string)
	return "ssh box " + what, what != ""
}

func TestNewKindOfTool(t *testing.T) {
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n" +
		`@reason("no vault") forbid(principal, action == Action::"call", resource) when { resource in Server::"vault" };` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			toolCall("c1", "stream", `{"what":"a"}`),
			toolCall("c2", "secret", `{"what":"b"}`),
			toolCall("c3", "remote", `{"what":"ls"}`),
		}},
		{Text: "fine"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Policy = pol
	a.Tools.Add(probeTool{name: "stream", streams: true})
	a.Tools.Add(probeTool{name: "secret", server: "vault", hidden: true})
	a.Tools.Add(remoteShell{probeTool{name: "remote"}})
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}

	req := prov.requests[0]
	for _, d := range req.Tools {
		if d.Name == "secret" {
			t.Error("a hidden tool was offered to the model")
		}
	}
	if !strings.Contains(req.System, "# Deferred tools") {
		t.Error("no note on the hidden tools")
	}
	if len(ui.lives) != 1 || ui.lives[0] != "⚙ stream a" {
		t.Errorf("live folds %q", ui.lives)
	}
	if r := j.es[2]; r.ToolCallID != "c1" || r.IsError || r.Output != "stream did a in "+cwd+"\n" {
		t.Errorf("streamed: %+v", r)
	}
	if r := j.es[3]; r.ToolCallID != "c2" || !r.IsError || r.Output != "denied by policy: no vault" {
		t.Errorf("the policy did not see the server: %+v", r)
	}
	if len(sh.handed) != 1 || sh.handed[0] != "c3\x00ssh box ls" {
		t.Errorf("handed off %q", sh.handed)
	}
	if !strings.Contains(ui.String(), "❯\x1b[0m \x1b[1mssh box ls") {
		t.Errorf("the command was not shown as one:\n%s", ui.String())
	}

	// Interrupted, the command left its output in the shell: the next
	// request closes the call with it.
	sh.outputs["c3"] = rpc.Output{Output: "partial"}
	if err := a.Start(context.Background(), "again", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[4]; r.ToolCallID != "c3" || r.Output != "partial\n[interrupted by the user]" {
		t.Errorf("closed call %+v", r)
	}
}
