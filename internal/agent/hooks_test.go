package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// hook puts an executable /bin/sh script into the agent's hooks directory.
func hook(t *testing.T, a *Agent, event, name, body string) {
	t.Helper()
	if a.Cfg.HooksDir == "" {
		a.Cfg.HooksDir = t.TempDir()
	}
	path := filepath.Join(a.Cfg.HooksDir, event, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The simplest guard: a pre-tool hook exiting non-zero denies the call,
// its stderr is the reason, on the terminal and for the model.
func TestPreToolExitDenies(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"sudo rm -rf /var/x"}`)}},
		{ToolCalls: []llm.ToolCall{toolCall("c2", "bash", `{"command":"ls"}`)}},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = ""
	hook(t, a, "pre-tool", "no-sudo", `grep -q sudo && { echo "sudo is not for the agent" >&2; exit 1; }; exit 0`)
	if err := a.Start(context.Background(), "clean up", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	const msg = "denied by hook no-sudo: sudo is not for the agent"
	if r := j.es[2]; r.Kind != session.KindToolResult || r.ToolCallID != "c1" || r.Output != msg || !r.IsError {
		t.Errorf("result %+v", r)
	}
	if !strings.Contains(ui.String(), "✗ "+msg) {
		t.Errorf("terminal:\n%s", ui.String())
	}
	if len(sh.handed) != 1 || sh.handed[0] != "c2\x00ls" {
		t.Errorf("handed off %q: the guard must let the next call pass", sh.handed)
	}
}

// A hook makes the verdict stricter only; arguments it replaces are what
// runs, and the policy checks them again.
func TestPreToolVerdicts(t *testing.T) {
	pol, err := policy.Load(context.Background(), "", policy.Rules{Deny: []string{"rm *"}, Ask: []string{"git push*"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command, reply string
		asked                bool
		handed, result       string
	}{
		{"allow does not undo ask", "git push", `{"action":"allow"}`, true, "git push", ""},
		{"ask on an allowed call", "make deploy", `{"action":"ask","reason":"deploys"}`, true, "make deploy", ""},
		{"deny with a reason", "make deploy", `{"action":"deny","reason":"not on friday"}`, false, "", "denied by hook h: not on friday"},
		{"replaced command runs", "git push --force", `{"args":{"command":"git push --force-with-lease"}}`, true, "git push --force-with-lease", ""},
		{"replaced command checked", "ls", `{"args":{"command":"rm -rf x"}}`, false, "", `denied by policy: matches "rm *"`},
	} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"`+tc.command+`"}`)}},
			{Text: "done"},
		}}
		a, j, sh, ui, cwd := newAgent(t, prov)
		a.Policy, a.Cfg.HooksDir, ui.answer = pol, "", "y"
		hook(t, a, "pre-tool", "h", "cat >/dev/null; echo '"+tc.reply+"'")
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		if asked := len(ui.asked) > 0; asked != tc.asked {
			t.Errorf("%s: asked %q", tc.name, ui.asked)
		}
		var handed string
		if len(sh.handed) > 0 {
			_, handed, _ = strings.Cut(sh.handed[0], "\x00")
		}
		if handed != tc.handed {
			t.Errorf("%s: handed off %q, want %q", tc.name, handed, tc.handed)
		}
		if tc.result != "" {
			if r := j.es[2]; r.Output != tc.result || !r.IsError {
				t.Errorf("%s: result %+v", tc.name, r)
			}
		}
	}
}

// A pre-tool hook sees the call as the policy does, with its verdict.
func TestPreToolInput(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"notes.txt"}`)}},
		{Text: "done"},
	}}
	a, _, _, _, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = ""
	got := filepath.Join(t.TempDir(), "in.json")
	hook(t, a, "pre-tool", "dump", `cat > "`+got+`"`)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "read", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Event, Tool, Cwd, Path, Session string
		Args                            map[string]any
		Policy                          struct{ Action string }
	}
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if in.Event != "pre-tool" || in.Tool != "read_file" || in.Cwd != cwd || !strings.HasSuffix(in.Path, "notes.txt") ||
		in.Args["path"] != "notes.txt" || in.Session != "s1" || in.Policy.Action != "allow" {
		t.Errorf("input %s", b)
	}
}

// The post-tool hook of the example masks secrets in what is recorded, of
// a built-in tool and of a command run by the shell; the terminal says so.
func TestPostToolMasks(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("the example needs jq")
	}
	example, err := os.ReadFile("../../examples/hooks/post-tool/mask-secrets")
	if err != nil {
		t.Fatal(err)
	}
	const key, token = "AKIAABCDEFGHIJKLMNOP", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"creds"}`), toolCall("c2", "bash", `{"command":"cat creds"}`)}},
		{Text: "done"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = t.TempDir()
	path := filepath.Join(a.Cfg.HooksDir, "post-tool", "mask-secrets")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, example, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "creds"), []byte("id="+key+"\ngh="+token+"\nplain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "show creds", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	sh.outputs["c2"] = rpc.Output{Output: "id=" + key + "\ngh=" + token + "\n", Cwd: cwd}
	if err := a.Resume(context.Background(), "c2", 0, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant tool_result tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	for _, r := range j.es[2:4] {
		if strings.Contains(r.Output, key) || strings.Contains(r.Output, token) ||
			!strings.Contains(r.Output, "AKIA***") || !strings.Contains(r.Output, "ghp_***") {
			t.Errorf("%s result not masked: %q", r.ToolName, r.Output)
		}
	}
	if !strings.HasSuffix(j.es[3].Output, "[exit 0, cwd "+cwd+"]") {
		t.Errorf("bash result %q", j.es[3].Output)
	}
	if n := strings.Count(ui.String(), "as post-tool/mask-secrets left it"); n != 2 {
		t.Errorf("%d notes of the replaced results:\n%s", n, ui.String())
	}
}

// A hook that fails or answers nonsense is told about and passed over.
func TestHookFailures(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"notes.txt"}`)}},
		{Text: "done"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = ""
	hook(t, a, "post-tool", "a-broken", "echo oops >&2; exit 3")
	hook(t, a, "post-tool", "b-chatty", "echo masked it")
	hook(t, a, "user-prompt", "typo", `echo '{"contxt":"x"}'`)
	if err := os.WriteFile(filepath.Join(a.Cfg.HooksDir, "post-tool", "c-not-executable"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "read", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if r := j.es[2]; !strings.Contains(r.Output, "hello") || r.IsError {
		t.Errorf("result %+v", r)
	}
	out := ui.String()
	for _, want := range []string{
		"hook post-tool/a-broken: exit 3: oops",
		"hook post-tool/b-chatty: the reply is not a JSON object",
		`hook user-prompt/typo: unknown key "contxt"`,
		"c-not-executable: not executable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q on the terminal:\n%s", want, out)
		}
	}
}

// What user-prompt hooks add goes with the request; a deny keeps it from
// the model and the journal.
func TestUserPrompt(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "on main"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = ""
	hook(t, a, "user-prompt", "branch", `echo '{"context":"branch: main"}'`)
	if err := a.Start(context.Background(), "which branch", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if e := j.es[0]; e.Kind != session.KindUser || !strings.HasPrefix(e.Text, "which branch\n\n<system-reminder>") || !strings.Contains(e.Text, "branch: main") {
		t.Errorf("request %q", e.Text)
	}
	if last := prov.requests[0].Messages[0]; !strings.Contains(last.Text, "user-prompt hook branch:\nbranch: main") {
		t.Errorf("the model got %q", last.Text)
	}
	if !strings.Contains(ui.String(), "(user-prompt/branch: branch: main)") {
		t.Errorf("terminal:\n%s", ui.String())
	}

	hook(t, a, "user-prompt", "guard", `grep -q password && echo '{"deny":"no passwords here"}'; exit 0`)
	n := len(j.es)
	if err := a.Start(context.Background(), "my password is hunter2", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(j.es) != n || len(prov.requests) != 1 {
		t.Errorf("a denied request went on: journal %s, %d requests", kinds(j.es), len(prov.requests))
	}
	if !strings.Contains(ui.String(), "✗ denied by hook guard: no passwords here") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// A stop hook learns how the request ended.
func TestStop(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"x"}`)}, InputTokens: 100, OutputTokens: 10},
		{Text: "all done", InputTokens: 150, OutputTokens: 5},
	}}
	a, _, _, _, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = ""
	got := filepath.Join(t.TempDir(), "in.json")
	hook(t, a, "stop", "log", `cat > "`+got+`"; echo not a reply`)
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if in["event"] != "stop" || in["text"] != "all done" || in["steps"] != 2.0 || in["input_tokens"] != 250.0 || in["output_tokens"] != 15.0 {
		t.Errorf("input %s", b)
	}
}
