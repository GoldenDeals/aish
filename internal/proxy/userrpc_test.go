package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// userCallsEnv holds the calls, a JSON list of rpc.Request, that the test
// binary, run as a client by TestUserOnly, makes at $AISH_SOCK; it tells
// how each ended in the file at peerOutEnv.
const userCallsEnv = "PROXY_TEST_USER_CALLS"

// asUser makes the test the shell's foreground job, as hosted does, for a
// proxy made without it: the context of a call the user makes at the
// prompt.
func asUser(p *Proxy) context.Context {
	p.mu.Lock()
	p.fg = func() (int, error) { return syscall.Getpgrp(), nil }
	p.mu.Unlock()
	return rpc.WithPeer(context.Background(), os.Getpid())
}

// TestUserHelper is not a test of its own: it is the client in
// TestUserOnly.
func TestUserHelper(t *testing.T) {
	calls := os.Getenv(userCallsEnv)
	if calls == "" {
		t.Skip("the client of TestUserOnly")
	}
	var b strings.Builder
	var reqs []rpc.Request
	err := json.Unmarshal([]byte(calls), &reqs)
	var c *rpc.Client
	if err == nil {
		c, err = rpc.FromEnv()
	}
	if err != nil {
		fmt.Fprintf(&b, "%v\n", err)
	}
	for _, r := range reqs {
		if err == nil {
			fmt.Fprintf(&b, "%s: %v\n", r.Method, c.Call(r.Method, r.Params, nil))
		}
	}
	_ = os.WriteFile(os.Getenv(peerOutEnv), []byte(b.String()), 0o600)
	os.Exit(0)
}

// `aish resume`, `aish clear`, `aish new` and `aish model` change what the
// shell runs at its next prompt — the other session's functions, aliases
// and variables — and the model that answers it: only the user, at the
// shell's foreground, may. Between requests nothing else refuses a
// background subagent's command, or a job the agent's command left;
// during one the assistant is refused as before. What only reads goes
// through from anywhere.
func TestUserOnly(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	p, _, _ := hosted(t, &scripted{})
	// Not session.New: its id would be this one's, made the same second.
	const other = "20000101-000000-1"
	if err := os.WriteFile(filepath.Join(p.sess.Dir(), other+".jsonl"), []byte(`{"kind":"user","text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := filepath.Join(p.run, "restore.bash")
	sock := filepath.Join(p.run, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go rpc.Serve(l, p.handle)

	// The client's process group, as a job of the shell has one: sleep
	// leads it, so that it is there before the client.
	lead := exec.Command(sleep, "60")
	lead.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := lead.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		lead.Process.Kill()
		lead.Wait()
	})
	group := lead.Process.Pid

	req := func(method string, params any) rpc.Request {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		return rpc.Request{Method: method, Params: b}
	}
	calls := func(fg int, reqs ...rpc.Request) string {
		t.Helper()
		p.mu.Lock()
		p.fg = func() (int, error) { return fg, nil }
		p.mu.Unlock()
		b, err := json.Marshal(reqs)
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "calls")
		c := exec.Command(exe, "-test.run=^TestUserHelper$")
		c.Env = []string{
			"HOME=" + os.Getenv("HOME"),
			"XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"),
			"AISH_CONFIG=" + os.Getenv("AISH_CONFIG"),
			"PATH=" + os.Getenv("PATH"),
			"AISH_SOCK=" + sock,
			userCallsEnv + "=" + string(b),
			peerOutEnv + "=" + out,
		}
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("the client: %v\n%s", err, b)
		}
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	expect := func(got string, want ...string) {
		t.Helper()
		if w := strings.Join(want, "\n") + "\n"; got != w {
			t.Errorf("the client got:\n%s\nwant:\n%s", got, w)
		}
	}

	resume := req(rpc.MethodResume, rpc.ResumeParams{ID: other})
	clr := req(rpc.MethodClear, rpc.ClearParams{})
	newSess := req(rpc.MethodClear, rpc.ClearParams{SaveNew: true})
	model := req(rpc.MethodModel, rpc.ModelParams{Model: "m2"})
	// The handler wrote them; the test reads them once the client is gone.
	now := func() (id, model string, saved bool) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.sess.ID, p.model, p.sess.Saved()
	}
	id, _, _ := now()
	unchanged := func(when string) {
		t.Helper()
		if sid, m, _ := now(); sid != id || m != "m" {
			t.Errorf("%s: session %s, model %q", when, sid, m)
		}
		if b, _ := os.ReadFile(restore); len(b) > 0 {
			t.Errorf("%s: restore.bash %q", when, b)
		}
	}

	// The shell's own group in the foreground: the user is at the prompt,
	// the client is a job in the background.
	got := calls(syscall.Getpgrp(), resume, clr, newSess, model,
		req(rpc.MethodInfo, nil), req(rpc.MethodStatus, nil), req(rpc.MethodHistory, nil),
		req(rpc.MethodFolds, nil), req(rpc.MethodTasks, rpc.TasksParams{}), req(rpc.MethodMCPStatus, nil))
	expect(got,
		rpc.MethodResume+": "+errNotShell.Error(),
		rpc.MethodClear+": "+errNotShell.Error(),
		rpc.MethodClear+": "+errNotShell.Error(),
		rpc.MethodModel+": "+errNotShell.Error(),
		rpc.MethodInfo+": <nil>",
		rpc.MethodStatus+": <nil>",
		rpc.MethodHistory+": <nil>",
		rpc.MethodFolds+": <nil>",
		rpc.MethodTasks+": <nil>",
		rpc.MethodMCPStatus+": <nil>",
	)
	unchanged("from the background")

	// A request in progress: the agent's command, in the foreground, or a
	// subagent's, in the background, is the assistant.
	p.marker(Marker{Kind: "ask-start"})
	for _, fg := range []int{group, syscall.Getpgrp()} {
		expect(calls(fg, resume, clr, newSess, model),
			rpc.MethodResume+": sessions are switched by the user, not by the assistant",
			rpc.MethodClear+": sessions are cleared by the user, not by the assistant",
			rpc.MethodClear+": sessions are cleared by the user, not by the assistant",
			rpc.MethodModel+": the model is switched by the user, not by the assistant",
		)
	}
	unchanged("during a request")
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})

	// The client's group in the foreground: the user typed the command.
	expect(calls(group, model), rpc.MethodModel+": <nil>")
	if _, m, _ := now(); m != "m2" {
		t.Errorf("aish model left %q", m)
	}
	expect(calls(group, resume), rpc.MethodResume+": <nil>")
	if sid, _, _ := now(); sid != other {
		t.Errorf("aish resume left session %s", sid)
	}
	if b, _ := os.ReadFile(restore); !strings.HasSuffix(string(b), "export 'AISH_SESSION="+other+"'\n") {
		t.Errorf("restore.bash %q", b)
	}
	expect(calls(group, clr), rpc.MethodClear+": <nil>")
	if sid, _, saved := now(); sid == other || saved {
		t.Errorf("aish clear left session %s, saved %v", sid, saved)
	}
	expect(calls(group, newSess), rpc.MethodClear+": <nil>")
	if sid, _, saved := now(); !saved {
		t.Errorf("aish new left session %s unsaved", sid)
	}
}
