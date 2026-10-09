package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// yoloRules deny rm and ask before git push, as config.toml's [policy] may.
func yoloRules(t *testing.T) *policy.Engine {
	t.Helper()
	dir := t.TempDir()
	src := "permit(principal, action, resource);\n" +
		`@reason("no sudo")` + "\nforbid(principal, action == Action::\"run\", resource == Command::\"sudo\");\n"
	if err := os.WriteFile(filepath.Join(dir, "p.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(context.Background(), dir, policy.Rules{Deny: []string{"rm *", "touch *"}, Ask: []string{"git push*"}})
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

func argsJSON(t *testing.T, args map[string]string) string {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// yoloCall has the agent under pol make one call of tool with args, aish
// yolo as on says. It returns what went to the shell, "" for nothing, the
// result recorded for the call, and the questions asked.
func yoloCall(t *testing.T, pol *policy.Engine, on bool, tool, args string, setup func(*Agent)) (handed, result string, asked []string) {
	t.Helper()
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", tool, args)}},
		{Text: "done"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Policy, a.Cfg.HooksDir = pol, ""
	a.Yolo = func() bool { return on }
	if setup != nil {
		setup(a)
	}
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(sh.handed) > 0 {
		_, handed, _ = strings.Cut(sh.handed[0], "\x00")
	}
	for _, e := range j.es {
		if e.Kind == session.KindToolResult {
			result = e.Output
		}
	}
	return handed, result, ui.asked
}

// Under aish yolo a call goes past the Cedar policies, the [policy] rules
// and the question; off, each of them stops it again.
func TestYoloLiftsPolicies(t *testing.T) {
	pol := yoloRules(t)
	for _, tc := range []struct{ command, off string }{
		{"rm x", `denied by policy: matches "rm *"`},
		{"sudo ls", "denied by policy: no sudo"},
		{"git push", "denied by policy: needs confirmation, no terminal"},
	} {
		args := argsJSON(t, map[string]string{"command": tc.command})
		handed, result, asked := yoloCall(t, pol, true, tools.Bash, args, nil)
		if handed != tc.command || result != "" || len(asked) != 0 {
			t.Errorf("%s under yolo: handed %q, result %q, asked %q", tc.command, handed, result, asked)
		}
		handed, result, _ = yoloCall(t, pol, false, tools.Bash, args, nil)
		if handed != "" || !strings.HasPrefix(result, tc.off) {
			t.Errorf("%s with yolo off: handed %q, result %q", tc.command, handed, result)
		}
	}
	// The question is not asked: the answer would be no.
	args := argsJSON(t, map[string]string{"command": "git push"})
	handed, _, asked := yoloCall(t, pol, true, tools.Bash, args, func(a *Agent) { a.UI.(*fakeUI).answer = "n" })
	if handed != "git push" || len(asked) != 0 {
		t.Errorf("ask under yolo: handed %q, asked %q", handed, asked)
	}
	_, _, asked = yoloCall(t, pol, false, tools.Bash, args, func(a *Agent) { a.UI.(*fakeUI).answer = "n" })
	if len(asked) != 1 {
		t.Errorf("ask with yolo off: asked %q", asked)
	}
}

// The guard holds under yolo: the agent does not trust a project, by aish
// trust, a write of trusted.json or arguments a hook gave.
func TestYoloKeepsGuard(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	pol := yoloRules(t)
	deny := "denied by policy: " + policy.TrustReason
	for _, command := range []string{"aish trust", "echo '{}' > ~/.local/share/aish/trusted.json"} {
		handed, result, _ := yoloCall(t, pol, true, tools.Bash, argsJSON(t, map[string]string{"command": command}), nil)
		if handed != "" || result != deny {
			t.Errorf("%s under yolo: handed %q, result %q", command, handed, result)
		}
	}
	if _, result, _ := yoloCall(t, pol, true, "write_file", `{"path":"x","content":"{}"}`, nil); strings.HasPrefix(result, "denied") {
		t.Fatalf("write_file x under yolo: %q", result)
	}
	var trust string
	_, result, _ := yoloCall(t, pol, true, "write_file", `{"path":"~/.local/share/aish/trusted.json","content":"{}"}`, func(*Agent) {
		trust = config.TrustFile() // of the home newAgent made
	})
	if result != deny {
		t.Errorf("write_file trusted.json under yolo: %q", result)
	}
	if _, err := os.Stat(trust); err == nil {
		t.Errorf("%s written", trust)
	}
	// A pre-tool hook's arguments are checked again, by the guard alone.
	handed, result, _ := yoloCall(t, pol, true, tools.Bash, `{"command":"ls"}`, func(a *Agent) {
		hook(t, a, "pre-tool", "h", `cat >/dev/null; echo '{"args":{"command":"aish trust"}}'`)
	})
	if handed != "" || result != deny {
		t.Errorf("aish trust from a hook under yolo: handed %q, result %q", handed, result)
	}
}

// The hooks run under yolo: a deny of theirs stays, and so do the
// arguments they give; their ask is no question either.
func TestYoloKeepsHooks(t *testing.T) {
	pol := yoloRules(t)
	for reply, want := range map[string]string{
		`{"action":"deny","reason":"not today"}`: "",
		`{"action":"ask","reason":"sure?"}`:      "make",
		`{"args":{"command":"rm -rf x"}}`:        "rm -rf x",
	} {
		handed, result, asked := yoloCall(t, pol, true, tools.Bash, `{"command":"make"}`, func(a *Agent) {
			hook(t, a, "pre-tool", "h", "cat >/dev/null; echo '"+reply+"'")
		})
		if handed != want || len(asked) != 0 {
			t.Errorf("hook %s under yolo: handed %q, result %q, asked %q", reply, handed, result, asked)
		}
		if want == "" && result != "denied by hook h: not today" {
			t.Errorf("hook %s under yolo: result %q", reply, result)
		}
	}
}

// subRunner is a provider whose subagents run one bash command each,
// commands[NAME], once their gate (if any) is open, and answer with its
// result; the host follows host.
func subRunner(host func(llm.Request) *llm.Response, commands map[string]string, gates map[string]chan struct{}) *subProvider {
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		name := subOf(req)
		if name == "" {
			return reply(host(req), onText)
		}
		if rs := lastUser(req).ToolResults; len(rs) > 0 {
			return reply(&llm.Response{Text: rs[0].Content}, onText)
		}
		if g := gates[name]; g != nil {
			select {
			case <-g:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		b, _ := json.Marshal(map[string]string{"command": commands[name]})
		return callOf("b1", tools.Bash, string(b)), nil
	}
	return prov
}

// A subagent's bash under yolo goes past its file's tools (Grep: only
// reads) and the rules, but not past the guard; with yolo off it is
// refused again.
func TestYoloSubagentBash(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	for _, on := range []bool{true, false} {
		commands := map[string]string{"alpha": "touch marker", "beta": "echo '{}' > ~/.local/share/aish/trusted.json"}
		prov := subRunner(host(`[{"agent":"alpha","prompt":"write"},{"agent":"beta","prompt":"trust"}]`), commands, nil)
		a, j, _, _, cwd := newSubAgent(t, prov, def("alpha", "Grep"), def("beta"))
		a.Policy = yoloRules(t)
		a.Yolo = func() bool { return on }
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(cwd, "marker"))
		if written := err == nil; written != on {
			t.Errorf("yolo %v: touch marker ran: %v (%s)", on, written, j.es[2].Output)
		}
		if !strings.Contains(j.es[2].Output, "## beta (ok)\ndenied by policy: "+policy.TrustReason) {
			t.Errorf("yolo %v: beta wrote trusted.json: %s", on, j.es[2].Output)
		}
		if _, err := os.Stat(config.TrustFile()); err == nil {
			t.Errorf("yolo %v: trusted.json written", on)
		}
	}
	// Grep's scope alone, without the rules.
	prov := subRunner(host(`[{"agent":"alpha","prompt":"write"}]`), map[string]string{"alpha": "mkdir made"}, nil)
	a, j, _, _, cwd := newSubAgent(t, prov, def("alpha", "Grep"))
	var on atomic.Bool
	a.Yolo = on.Load
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(j.es[2].Output, "not run: mkdir is not among the commands") {
		t.Errorf("Grep's scope with yolo off: %s", j.es[2].Output)
	}
}

// A subagent in the background, started before aish yolo, follows the
// switch: on, its next call goes; off again, the next one is checked.
func TestYoloBackgroundSubagent(t *testing.T) {
	gates := map[string]chan struct{}{"alpha": make(chan struct{}), "beta": make(chan struct{})}
	commands := map[string]string{"alpha": "touch alpha", "beta": "touch beta"}
	prov := subRunner(nil, commands, gates)
	a, _, _, _, cwd := newBgAgent(t, prov, def("alpha", "Grep"), def("beta", "Grep"))
	a.Policy = yoloRules(t)
	var on atomic.Bool
	a.Yolo = on.Load
	mustUse(t, a, subName, `{"tasks":[{"agent":"alpha","prompt":"x"},{"agent":"beta","prompt":"y"}],"background":true}`)

	on.Store(true)
	close(gates["alpha"])
	if got := mustUse(t, a, taskWait, `{"ids":["bg1"],"timeout":10}`); !strings.HasPrefix(got, "## bg1 alpha (ok)\n[exit 0") {
		t.Errorf("alpha under yolo: %q", got)
	}
	if _, err := os.Stat(filepath.Join(cwd, "alpha")); err != nil {
		t.Errorf("alpha's command under yolo: %v", err)
	}

	on.Store(false)
	close(gates["beta"])
	if got := mustUse(t, a, taskWait, `{"ids":["bg2"],"timeout":10}`); !strings.Contains(got, `denied by policy: matches "touch *"`) {
		t.Errorf("beta with yolo off: %q", got)
	}
	if _, err := os.Stat(filepath.Join(cwd, "beta")); err == nil {
		t.Error("beta's command ran with yolo off")
	}
}

// The switch is read at each call, from the goroutines of the subagents
// too: -race tells if it is not safe for them.
func TestYoloReadAtEachCall(t *testing.T) {
	var mu sync.Mutex
	n := 0
	a := &Agent{Yolo: func() bool {
		mu.Lock()
		defer mu.Unlock()
		n++
		return n%2 == 1
	}}
	if !a.yolo() || a.yolo() || !a.yolo() {
		t.Error("the switch was not asked at each call")
	}
	if (&Agent{}).yolo() {
		t.Error("no switch is yolo on")
	}
}
