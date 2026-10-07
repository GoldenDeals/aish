package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/tools"
)

// script writes an executable /bin/sh script at dir/event/name.
func script(t *testing.T, dir, event, name, body string) Hook {
	t.Helper()
	path := filepath.Join(dir, event, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Hook{Event: event, Name: name, Path: path}
}

func names(hs []Hook) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.String())
	}
	return strings.Join(out, " ")
}

func TestFindNone(t *testing.T) {
	dir := t.TempDir()
	for _, dirs := range []string{"", filepath.Join(dir, "missing"), dir} {
		s, problems := Find(dirs)
		if len(problems) > 0 {
			t.Errorf("Find(%q): %v", dirs, problems)
		}
		for _, e := range Events {
			if hs := s.For(e); len(hs) > 0 {
				t.Errorf("Find(%q) found %s", dirs, names(hs))
			}
		}
	}
	if hs := (*Set)(nil).For(PreTool); hs != nil {
		t.Errorf("nil set: %v", hs)
	}
}

// The user's hooks run before the project's, each event's by name; what
// is skipped by mistake is told.
func TestFind(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	script(t, user, PreTool, "b-guard", "exit 0")
	script(t, user, PreTool, "a-log", "exit 0")
	script(t, user, PostTool, "mask", "exit 0")
	script(t, project, PreTool, "a-project", "exit 0")
	script(t, user, PreTool, ".hidden", "exit 0")
	script(t, user, PreTool, "backup~", "exit 0")
	if err := os.WriteFile(filepath.Join(user, PreTool, "README"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	script(t, user, "pretool", "typo", "exit 0")
	if err := os.WriteFile(filepath.Join(user, "lib.sh"), []byte("x() { :; }"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, problems := Find(user + string(filepath.ListSeparator) + project)
	if got, want := names(s.For(PreTool)), "pre-tool/a-log pre-tool/b-guard pre-tool/a-project"; got != want {
		t.Errorf("pre-tool hooks %q, want %q", got, want)
	}
	if got := names(s.For(PostTool)); got != "post-tool/mask" {
		t.Errorf("post-tool hooks %q", got)
	}
	var msgs []string
	for _, p := range problems {
		msgs = append(msgs, p.Error())
	}
	all := strings.Join(msgs, "\n")
	if len(problems) != 2 || !strings.Contains(all, "README: not executable") || !strings.Contains(all, "pretool: no such event") {
		t.Errorf("problems:\n%s", all)
	}
}

// A hook reads the event and its input on stdin, in the shell's directory
// with the shell's environment.
func TestRunInput(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	got := filepath.Join(dir, "got")
	h := script(t, dir, PreTool, "dump", `cat > "`+got+`"; pwd >> "`+got+`"; printf '%s\n' "$FOO" >> "`+got+`"`)
	in := struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}{"bash", map[string]any{"command": "ls"}}
	r := h.Run(context.Background(), tools.Exec{Dir: cwd, Env: []string{"FOO=from the shell", "PATH=/usr/bin:/bin"}}, in)
	if r.Err != nil || r.Exit != 0 {
		t.Fatalf("run: %+v", r)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	stdin, rest, _ := strings.Cut(string(b), "\n")
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdin), &obj); err != nil {
		t.Fatalf("stdin %q: %v", stdin, err)
	}
	if obj["event"] != PreTool || obj["tool"] != "bash" || obj["args"].(map[string]any)["command"] != "ls" {
		t.Errorf("stdin %s", stdin)
	}
	real, _ := filepath.EvalSymlinks(cwd)
	if want := real + "\nfrom the shell\n"; rest != want && rest != cwd+"\nfrom the shell\n" {
		t.Errorf("cwd and env %q, want %q", rest, want)
	}
}

func TestRunReply(t *testing.T) {
	dir := t.TempDir()
	run := func(event, body string) Result {
		t.Helper()
		return script(t, dir, event, "h", body).Run(context.Background(), tools.Exec{}, nil)
	}

	r := run(PreTool, `echo '{"action":"ask","reason":"pushes","args":{"command":"git push --dry-run"}}'`)
	if r.Err != nil || r.Reply.Action != "ask" || r.Reply.Reason != "pushes" || r.Reply.Args["command"] != "git push --dry-run" {
		t.Errorf("pre-tool reply %+v", r)
	}
	r = run(PostTool, `echo '{"output":""}'`)
	if r.Err != nil || r.Reply.Output == nil || *r.Reply.Output != "" {
		t.Errorf("an empty output must replace the result: %+v", r)
	}
	r = run(UserPrompt, `echo '{"context":"on branch main"}'`)
	if r.Err != nil || r.Reply.Context != "on branch main" || r.Reply.Deny != nil {
		t.Errorf("user-prompt reply %+v", r)
	}
	r = run(UserPrompt, `printf '{"deny": "no secrets"}\n\n'`)
	if r.Err != nil || r.Reply.Deny == nil || *r.Reply.Deny != "no secrets" {
		t.Errorf("user-prompt deny %+v", r)
	}
	if r = run(PostTool, `printf '\n'`); r.Err != nil || r.Reply.Output != nil {
		t.Errorf("no reply must change nothing: %+v", r)
	}
	if r = run(Stop, `echo done`); r.Err != nil {
		t.Errorf("what a stop hook prints is not a reply: %v", r.Err)
	}
	for _, tc := range []struct{ event, body, err string }{
		{PostTool, `echo masked`, "not a JSON object"},
		{PostTool, `echo '{"ouput":"x"}'`, `unknown key "ouput"`},
		{PostTool, `echo '{"action":"deny"}'`, `unknown key "action"`},
		{PreTool, `echo '{"action":"block"}'`, `action "block"`},
		{PreTool, `echo '{"args":"ls"}'`, "the reply"},
	} {
		if r := run(tc.event, tc.body); r.Err == nil || !strings.Contains(r.Err.Error(), tc.err) {
			t.Errorf("%s %s: error %v, want %q", tc.event, tc.body, r.Err, tc.err)
		}
	}
}

// A hook that fails gives its status and stderr; what it printed is no
// reply.
func TestRunExit(t *testing.T) {
	h := script(t, t.TempDir(), PreTool, "no-sudo", `grep -q sudo && { echo '{"action":"allow"}'; echo "sudo is not for the agent" >&2; exit 1; }; exit 0`)
	r := h.Run(context.Background(), tools.Exec{}, map[string]any{"args": map[string]any{"command": "sudo ls"}})
	if r.Err != nil || r.Exit != 1 || r.Stderr != "sudo is not for the agent" || r.Reply.Action != "" {
		t.Errorf("failed hook %+v", r)
	}
	r = h.Run(context.Background(), tools.Exec{}, map[string]any{"args": map[string]any{"command": "ls"}})
	if r.Err != nil || r.Exit != 0 {
		t.Errorf("passing hook %+v", r)
	}
	h = script(t, t.TempDir(), PreTool, "killed", `kill -TERM $$`)
	if r := h.Run(context.Background(), tools.Exec{}, nil); r.Err != nil || r.Exit != 128+15 {
		t.Errorf("killed hook %+v, want exit 143", r)
	}
	if r := (Hook{Event: PreTool, Name: "gone", Path: "/nonexistent/hook"}).Run(context.Background(), tools.Exec{}, nil); r.Err == nil {
		t.Errorf("a hook that cannot start must be an error: %+v", r)
	}
}

// A hook past its time is killed with whatever it started, and the call
// returns at once.
func TestRunTimeout(t *testing.T) {
	timeout = 200 * time.Millisecond
	t.Cleanup(func() { timeout = Timeout })
	dir := t.TempDir()
	for _, body := range []string{
		"sleep 30",
		"sleep 30 & wait",                   // a child holding stdout
		"trap '' TERM; while :; do :; done", // deaf to SIGTERM
	} {
		h := script(t, dir, PostTool, "slow", body)
		start := time.Now()
		r := h.Run(context.Background(), tools.Exec{}, nil)
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("%q: took %v", body, took)
		}
		if r.Err == nil || !strings.Contains(r.Err.Error(), "timed out") {
			t.Errorf("%q: %+v", body, r)
		}
	}

	// Done, but a child it left keeps stdout open: the reply counts.
	h := script(t, dir, PostTool, "notify", `sleep 5 & echo '{"output":"x"}'`)
	start := time.Now()
	r := h.Run(context.Background(), tools.Exec{}, nil)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("left child: took %v", took)
	}
	if r.Err != nil || r.Reply.Output == nil || *r.Reply.Output != "x" {
		t.Errorf("left child: %+v", r)
	}
}

func TestRunCanceled(t *testing.T) {
	h := script(t, t.TempDir(), PreTool, "slow", "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	r := h.Run(ctx, tools.Exec{}, nil)
	if !errors.Is(r.Err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Errorf("canceled: %+v after %v", r, time.Since(start))
	}
}
