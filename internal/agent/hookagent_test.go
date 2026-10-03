package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/tools"
)

// A pre-tool hook is told which subagent makes a call, as the policy is:
// "agent" is reviewer's name in its calls, the arguments a hook replaced
// included, and the host's calls have none.
func TestPreToolSeesSubagent(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if !strings.Contains(req.System, "BODY-reviewer") {
			return reply(host(`[{"agent":"reviewer","prompt":"read"}]`)(req), onText)
		}
		if len(lastUser(req).ToolResults) > 0 {
			return reply(&llm.Response{Text: "read"}, onText)
		}
		return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("r1", "read_file", `{"path":"notes.txt"}`)}}, nil
	}
	a, _, _, _, cwd := newSubAgent(t, prov, def("reviewer"))
	for _, name := range []string{"notes.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := filepath.Join(t.TempDir(), "in.jsonl")
	dump := `cat >> "` + got + `"; echo >> "` + got + `"`
	hook(t, a, "pre-tool", "a-dump", dump)
	hook(t, a, "pre-tool", "b-swap", `case "$(cat)" in *'"tool":"read_file"'*) echo '{"args":{"path":"other.txt"}}';; esac`)
	hook(t, a, "pre-tool", "c-dump", dump)
	if err := a.Start(context.Background(), "review", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	var task, read []map[string]any
	for line := range strings.Lines(string(b)) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var in map[string]any
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		switch in["tool"] {
		case subName:
			task = append(task, in)
		case "read_file":
			read = append(read, in)
		}
	}
	if len(task) != 2 || len(read) != 2 {
		t.Fatalf("inputs of the hooks:\n%s", b)
	}
	for _, in := range task {
		if agent, ok := in["agent"]; ok {
			t.Errorf("the host's call of task has agent %v", agent)
		}
	}
	for i, in := range read {
		if in["agent"] != "reviewer" || in["session"] != "sub:reviewer" {
			t.Errorf("reviewer's call to hook %d: agent %v, session %v", i, in["agent"], in["session"])
		}
	}
	if args, _ := read[1]["args"].(map[string]any); args["path"] != "other.txt" {
		t.Errorf("arguments after b-swap: %v", read[1]["args"])
	}
}
