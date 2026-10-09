package agent

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/hooks"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/tools"
)

var hookEvents = []string{hooks.UserPrompt, hooks.PreTool, hooks.PostTool, hooks.Stop}

// dumpHooks gives a hook on each event that keeps its input in a file of
// its own in the directory returned: the runs of subagents call them at
// once.
func dumpHooks(t *testing.T, a *Agent) string {
	t.Helper()
	dir := t.TempDir()
	for _, ev := range hookEvents {
		hook(t, a, ev, "dump", `f=$(mktemp "`+dir+`/in.XXXXXX") && cat > "$f"`)
	}
	return dir
}

// hookInputs are the inputs dumpHooks kept.
func hookInputs(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "in.*"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var in map[string]any
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		out = append(out, in)
	}
	return out
}

// readOnce is a subagent that reads notes.txt and then answers.
func readOnce(req llm.Request, onText func(string)) (*llm.Response, error) {
	if len(lastUser(req).ToolResults) > 0 {
		return reply(&llm.Response{Text: subOf(req) + " reply"}, onText)
	}
	return &llm.Response{ToolCalls: []llm.ToolCall{toolCall("r1", "read_file", `{"path":"notes.txt"}`)}}, nil
}

// runsOf checks the inputs of the hooks: the host's events, of session
// s1, have no subagent fields; those of alpha's runs name alpha and the
// host's session in every event. It returns the events of each run, by
// agent_id.
func runsOf(t *testing.T, ins []map[string]any) map[string][]string {
	t.Helper()
	runs := map[string][]string{}
	var host []string
	for _, in := range ins {
		ev, _ := in["event"].(string)
		switch in["session"] {
		case "s1":
			host = append(host, ev)
			for _, k := range []string{"agent", "agent_id", "parent_session"} {
				if v, ok := in[k]; ok {
					t.Errorf("the host's %s has %s %v", ev, k, v)
				}
			}
		case "sub:alpha":
			if in["agent"] != "alpha" || in["parent_session"] != "s1" {
				t.Errorf("alpha's %s: agent %v, parent_session %v", ev, in["agent"], in["parent_session"])
			}
			id, _ := in["agent_id"].(string)
			runs[id] = append(runs[id], ev)
		default:
			t.Errorf("%s of session %v", ev, in["session"])
		}
	}
	slices.Sort(host)
	if want := slices.Sorted(slices.Values(hookEvents)); !slices.Equal(host, want) {
		t.Errorf("the host's events %v, want %v", host, want)
	}
	for id, evs := range runs {
		slices.Sort(evs)
		if want := slices.Sorted(slices.Values(hookEvents)); !slices.Equal(evs, want) {
			t.Errorf("run %q: events %v, want %v", id, evs, want)
		}
	}
	return runs
}

// Two runs of one subagent in a call share the session sub:NAME, and the
// hooks tell them apart by agent_id, the call of task and the number of
// the task in it; every event of theirs names the subagent and the host's
// session.
func TestSubagentHookIDs(t *testing.T) {
	prov := &subProvider{}
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(host(`[{"agent":"alpha","prompt":"one"},{"agent":"alpha","prompt":"two"}]`)(req), onText)
		}
		return readOnce(req, onText)
	}
	a, _, _, _, cwd := newSubAgent(t, prov, def("alpha"))
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := dumpHooks(t, a)
	if err := a.Start(context.Background(), "do both", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	runs := runsOf(t, hookInputs(t, dir))
	if ids := slices.Sorted(maps.Keys(runs)); !slices.Equal(ids, []string{"t1.1", "t1.2"}) {
		t.Errorf("agent_id of the runs %q", ids)
	}
}

// One in the background is known to its hooks as to task_wait: bgN.
func TestBackgroundSubagentHookID(t *testing.T) {
	prov := &subProvider{}
	turns := hostTurns(
		callOf("t1", subName, `{"tasks":[{"agent":"alpha","prompt":"one"}],"background":true}`),
		&llm.Response{Text: "started it"},
	)
	prov.answer = func(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		if subOf(req) == "" {
			return reply(turns(req), onText)
		}
		return readOnce(req, onText)
	}
	a, _, _, _, cwd := newBgAgent(t, prov, def("alpha"))
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := dumpHooks(t, a)
	if err := a.Start(context.Background(), "start alpha", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":5}`); !strings.HasPrefix(got, "## bg1 alpha (ok)") {
		t.Fatalf("task_wait: %q", got)
	}
	runs := runsOf(t, hookInputs(t, dir))
	if ids := slices.Sorted(maps.Keys(runs)); !slices.Equal(ids, []string{"bg1"}) {
		t.Errorf("agent_id of the run %q", ids)
	}
}
