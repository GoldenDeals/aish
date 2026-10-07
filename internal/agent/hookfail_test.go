package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// hookFailRun asks for `ls` with the pre-tool hook guard of body in force
// and hooks_fail set to fail. It returns what came of the call: "" when
// it went to the shell, else the result the model got for it, a deny.
func hookFailRun(t *testing.T, fail, body string) (string, *fakeUI) {
	t.Helper()
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"ls"}`)}},
		{Text: "done"},
	}}
	a, j, sh, ui, cwd := newAgent(t, prov)
	a.Cfg.HooksDir, a.Cfg.HooksFail = "", fail
	hook(t, a, "pre-tool", "guard", body)
	if err := a.Start(context.Background(), "list", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(sh.handed) == 1 && sh.handed[0] == "c1\x00ls" {
		return "", ui
	}
	if len(sh.handed) > 0 || len(j.es) < 3 {
		t.Fatalf("handed off %q, journal %s", sh.handed, kinds(j.es))
	}
	r := j.es[2]
	if r.Kind != session.KindToolResult || r.ToolCallID != "c1" || !r.IsError {
		t.Fatalf("result %+v", r)
	}
	return r.Output, ui
}

// A pre-tool hook that gives no answer passes the call over by default,
// as Claude Code's do; with hooks_fail = "deny" it denies it, as a policy
// that fails does. Either way the terminal says what went wrong with the
// hook. A hook exiting non-zero answers: a deny under both.
func TestPreToolHookFails(t *testing.T) {
	for _, tc := range []struct {
		name, fail, body string
		result           string // "" when the call runs
	}{
		{"not JSON, allow", "allow", "cat >/dev/null; echo nonsense", ""},
		{"not JSON, empty", "", "cat >/dev/null; echo nonsense", ""},
		{"not JSON, deny", "deny", "cat >/dev/null; echo nonsense",
			"denied by hook guard: hook failed: the reply is not a JSON object"},
		{"unknown key, deny", "deny", `cat >/dev/null; echo '{"verdict":"no"}'`,
			`denied by hook guard: hook failed: unknown key "verdict"`},
		{"exit 1, allow", "allow", "cat >/dev/null; echo 'not today' >&2; exit 1", "denied by hook guard: not today"},
		{"exit 1, deny", "deny", "cat >/dev/null; echo 'not today' >&2; exit 1", "denied by hook guard: not today"},
		{"allow, deny", "deny", `cat >/dev/null; echo '{"action":"allow"}'`, ""},
	} {
		got, ui := hookFailRun(t, tc.fail, tc.body)
		if tc.result == "" && got != "" || !strings.HasPrefix(got, tc.result) {
			t.Errorf("%s: result %q, want %q", tc.name, got, tc.result)
		}
		failed := strings.Contains(ui.String(), "[aish: hook pre-tool/guard: ")
		if want := strings.Contains(tc.name, "JSON") || strings.Contains(tc.name, "key"); failed != want {
			t.Errorf("%s: a line about the failed hook: %v, want %v:\n%s", tc.name, failed, want, ui.String())
		}
	}
}

// A hook that cannot start denies under hooks_fail = "deny".
func TestPreToolHookCannotStart(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"ls"}`)}},
		{Text: "done"},
	}}
	a, j, sh, _, cwd := newAgent(t, prov)
	a.Cfg.HooksDir, a.Cfg.HooksFail = t.TempDir(), "deny"
	path := filepath.Join(a.Cfg.HooksDir, "pre-tool", "guard")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/nonexistent/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), "list", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(sh.handed) > 0 {
		t.Fatalf("handed off %q", sh.handed)
	}
	if r := j.es[2]; r.Kind != session.KindToolResult || !r.IsError || !strings.HasPrefix(r.Output, "denied by hook guard: hook failed: ") {
		t.Errorf("result %+v", r)
	}
}

// A hook that hangs is killed at hooks.Timeout; under hooks_fail = "deny"
// the call it held is denied. The timeout is the real one: this package
// cannot shorten it, so the test takes its five seconds.
func TestPreToolHookHangs(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the hook timeout")
	}
	start := time.Now()
	got, ui := hookFailRun(t, "deny", "sleep 30")
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("took %v", took)
	}
	if !strings.HasPrefix(got, "denied by hook guard: hook failed: timed out after ") {
		t.Errorf("result %q", got)
	}
	if !strings.Contains(ui.String(), "[aish: hook pre-tool/guard: timed out after ") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}
