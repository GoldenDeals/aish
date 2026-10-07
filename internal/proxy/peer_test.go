package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// What the test binary, run as a client by TestForegroundOnly, asks the
// proxy at $AISH_SOCK, from which directory, and the file it tells how
// each call ended in.
const (
	peerCallsEnv = "PROXY_TEST_PEER_CALLS"
	peerCwdEnv   = "PROXY_TEST_PEER_CWD"
	peerOutEnv   = "PROXY_TEST_PEER_OUT"
)

// TestPeerHelper is not a test of its own: it is the client in
// TestForegroundOnly.
func TestPeerHelper(t *testing.T) {
	calls := os.Getenv(peerCallsEnv)
	if calls == "" {
		t.Skip("the client of TestForegroundOnly")
	}
	var b strings.Builder
	c, err := rpc.FromEnv()
	for _, m := range strings.Fields(calls) {
		if err == nil {
			ap := rpc.AgentParams{Text: "hello", ID: "c1", Cwd: os.Getenv(peerCwdEnv), Env: os.Environ()}
			fmt.Fprintf(&b, "%s: %v\n", m, c.CallContext(context.Background(), m, ap, nil))
		} else {
			fmt.Fprintf(&b, "%s: %v\n", m, err)
		}
	}
	_ = os.WriteFile(os.Getenv(peerOutEnv), []byte(b.String()), 0o600)
	os.Exit(0)
}

// A request starts, goes on and is compacted only from the process group
// in the foreground of the shell's terminal: between requests nothing else
// refuses a background subagent's command. The MCP calls of its tools'
// wrappers go through from anywhere.
func TestForegroundOnly(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	p, _, cwd := hosted(t, &scripted{replies: []*llm.Response{{Text: "hi"}, {Text: "summed up"}}})
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

	calls := func(fg int, methods ...string) string {
		t.Helper()
		p.mu.Lock()
		p.fg = func() (int, error) { return fg, nil }
		p.mu.Unlock()
		out := filepath.Join(t.TempDir(), "calls")
		c := exec.Command(exe, "-test.run=^TestPeerHelper$")
		// The environment the request gets: hosted's, none of the machine's.
		c.Env = []string{
			"HOME=" + os.Getenv("HOME"),
			"XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"),
			"AISH_CONFIG=" + os.Getenv("AISH_CONFIG"),
			"PATH=" + os.Getenv("PATH"),
			"AISH_SOCK=" + sock,
			peerCallsEnv + "=" + strings.Join(methods, " "),
			peerCwdEnv + "=" + cwd,
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

	// The shell's own group in the foreground: the user is at the prompt.
	got := calls(syscall.Getpgrp(), rpc.MethodAgentStart, rpc.MethodCompact, rpc.MethodMCPList)
	expect(got,
		rpc.MethodAgentStart+": "+errNotShell.Error(),
		rpc.MethodCompact+": "+errNotShell.Error(),
		rpc.MethodMCPList+": <nil>",
	)
	if got := journalKinds(p.sess); got != "" {
		t.Fatalf("journal %q", got)
	}
	p.mu.Lock()
	p.handed = "c1"
	p.mu.Unlock()
	expect(calls(syscall.Getpgrp(), rpc.MethodAgentResume), rpc.MethodAgentResume+": "+errNotShell.Error())
	p.mu.Lock()
	p.handed = ""
	p.mu.Unlock()

	// The client's group in the foreground: a job the shell runs.
	got = calls(group, rpc.MethodAgentStart, rpc.MethodCompact, rpc.MethodMCPList)
	expect(got,
		rpc.MethodAgentStart+": <nil>",
		rpc.MethodCompact+": <nil>",
		rpc.MethodMCPList+": <nil>",
	)
	if got := journalKinds(p.sess); got != "user assistant summary" {
		t.Errorf("journal %q", got)
	}
}

// Without the client's pid, or without the shell's terminal, nothing is
// the shell's foreground.
func TestForegroundUnknown(t *testing.T) {
	p, _, cwd := hosted(t, &scripted{replies: []*llm.Response{{Text: "hi"}}})
	ap := rpc.AgentParams{Text: "hello", Cwd: cwd}
	b, _ := json.Marshal(ap)
	for _, method := range []string{rpc.MethodAgentStart, rpc.MethodCompact} {
		if _, err := p.handle(context.Background(), method, b); !errors.Is(err, errNotShell) {
			t.Errorf("%s without the client's pid: %v", method, err)
		}
	}
	p.mu.Lock()
	fg := p.fg
	p.fg = nil
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodAgentStart, ap); !errors.Is(err, errNotShell) {
		t.Errorf("agent_start before the shell runs: %v", err)
	}
	p.mu.Lock()
	p.fg = func() (int, error) { return 0, syscall.EBADF }
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodAgentStart, ap); !errors.Is(err, errNotShell) {
		t.Errorf("agent_start with the terminal gone: %v", err)
	}
	if got := journalKinds(p.sess); got != "" {
		t.Fatalf("journal %q", got)
	}
	p.mu.Lock()
	p.fg = fg
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodAgentStart, ap); err != nil {
		t.Errorf("agent_start from the shell's foreground: %v", err)
	}
}
